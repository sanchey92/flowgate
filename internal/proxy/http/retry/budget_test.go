package retry

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Файл намеренно в пакете retry, а не retry_test: публичный API бюджета
// не принимает время (и не должен — см. мануал Stage F), поэтому окно и
// ротацию бакетов тестируем white-box, записывая счётчики в нужный момент
// прошлого через bucketLocked.

// seedAt пишет счётчики в бакет момента at — тестовая инъекция времени.
func seedAt(b *Budget, at time.Time, total, retried int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bkt := b.bucketLocked(at)
	bkt.total += total
	bkt.retried += retried
}

// sumAt возвращает согласованную пару счётчиков окна на момент at.
func sumAt(b *Budget, at time.Time) (total, retried int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sumLocked(at)
}

// Пока в окне меньше budgetMinRequest запросов, доля не считается и ретраи
// свободные — иначе тихий роут с первым же упавшим запросом получил бы
// 1/1 = 100% > 20% и остался бы без ретраев вообще.
func TestBudget_AllowsWhileWindowIsCold(t *testing.T) {
	t.Parallel()
	b := NewBudget(20)

	for i := 0; i < budgetMinRequest-1; i++ {
		b.RecordRequest()
		assert.True(t, b.AllowRetry())
		assert.True(t, b.AllowRetry())
	}
}

// Сценарий retry storm из мануала §7: все бэкенды отвечают 503, каждый запрос
// хочет два ретрая (max_retries: 2). Девять холодных запросов списывают 18
// ретраев свободно, а десятый прогревает окно — и (18+1)*100 > 20*(10+1)
// пробивает потолок с порога: оба ретрая получают отказ.
func TestBudget_StormDeniedOnceWarm(t *testing.T) {
	t.Parallel()
	b := NewBudget(20)

	for i := 0; i < budgetMinRequest-1; i++ {
		b.RecordRequest()
		require.True(t, b.AllowRetry())
		require.True(t, b.AllowRetry())
	}

	b.RecordRequest()
	assert.False(t, b.AllowRetry())
	assert.False(t, b.AllowRetry())
}

// В затяжном шторме бюджет не выключает ретраи насовсем: отказанные попытки
// не записываются, total растёт — и доля периодически опускается ниже
// потолка. Это «постоянный зонд» из мануала §7: ~20% запросов продолжают
// пробовать и первыми заметят выздоровление бэкендов.
func TestBudget_SteadyStateKeepsRetryShare(t *testing.T) {
	t.Parallel()
	const requests = 200
	b := NewBudget(20)

	allowed := 0
	for i := 0; i < requests; i++ {
		b.RecordRequest()
		if b.AllowRetry() {
			allowed++
		}
	}

	assert.InDelta(t, 0.20, float64(allowed)/requests, 0.05)
}

// Краевое значение percent: 0 — честный запрет ретраев, как только роут
// прогрет. Валидация конфига пропускает ноль сознательно (мануал §5.1).
func TestBudget_PercentZeroDeniesWhenWarm(t *testing.T) {
	t.Parallel()
	b := NewBudget(0)

	for i := 0; i < budgetMinRequest; i++ {
		b.RecordRequest()
	}

	assert.False(t, b.AllowRetry())
}

// Краевое значение percent: 100 — самый щедрый режим, но не выключенный
// бюджет: разрешено, пока r ≤ t. Стартуя с r=0 при t=100, это ровно 101
// успешное списание (формула упреждающая, последнее разрешение даёт r=t+1).
func TestBudget_PercentHundredCapsAtRetryPerRequest(t *testing.T) {
	t.Parallel()
	const requests = 100
	b := NewBudget(100)

	for i := 0; i < requests; i++ {
		b.RecordRequest()
	}

	allowed := 0
	for b.AllowRetry() {
		allowed++
		if allowed > 2*requests {
			break // защита от вечного цикла при регрессии формулы
		}
	}

	assert.Equal(t, requests+1, allowed)
}

// Окно забывает прошлое само: шторм, закончившийся больше budgetWindow назад,
// не должен душить ретраи сейчас (мануал §3, «почему скользящее окно, а не
// счётчики с рестарта»). Контрольный кейс — тот же шторм внутри окна.
func TestBudget_WindowForgetsOldStorm(t *testing.T) {
	t.Parallel()
	now := time.Now()

	old := NewBudget(20)
	seedAt(old, now.Add(-15*time.Second), 50, 50)
	seedAt(old, now.Add(-time.Second), budgetMinRequest, 0)
	assert.True(t, old.AllowRetry(), "шторм за пределами окна не должен учитываться")

	recent := NewBudget(20)
	seedAt(recent, now.Add(-5*time.Second), 50, 50)
	assert.False(t, recent.AllowRetry(), "шторм внутри окна должен исчерпывать бюджет")
}

// Ячейка кольца переиспользуется ровно через budgetWindow: запись нового
// epoch обнуляет чужие данные, а сумма не верит непочищенным бакетам
// (ленивая очистка + фильтр minEpoch, мануал «Гонка №3»).
func TestBudget_BucketReuseDropsStaleCounters(t *testing.T) {
	t.Parallel()
	now := time.Now()

	// Перезаписанная ячейка: epoch отличается ровно на budgetBuckets,
	// то есть это тот же слот кольца.
	b := NewBudget(20)
	seedAt(b, now.Add(-budgetWindow), 100, 100)
	seedAt(b, now, 1, 0)
	total, retried := sumAt(b, now)
	assert.Equal(t, int64(1), total)
	assert.Zero(t, retried)

	// Непереписанная ячейка: данные старше окна отфильтровывает minEpoch.
	c := NewBudget(20)
	seedAt(c, now.Add(-budgetWindow), 100, 100)
	total, retried = sumAt(c, now)
	assert.Zero(t, total)
	assert.Zero(t, retried)
}

// F3: конкурентный стресс. Проверяем два инварианта связки «проверка+списание
// под одним мьютексом»: ни один инкремент не теряется, и бюджет не
// перерасходуется. Раздельные Allow/RecordRetry прошли бы -race (данные под
// мьютексом), но провалили бы границу ниже — гонка check-then-act логическая,
// и ловится только таким тестом (мануал, «Гонка №1»).
func TestBudget_ConcurrentConsistency(t *testing.T) {
	t.Parallel()
	const (
		goroutines = 100
		perG       = 50
		percent    = 20
	)
	b := NewBudget(percent)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				b.RecordRequest()
				if b.AllowRetry() {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	total, retried := sumAt(b, time.Now())

	assert.Equal(t, int64(goroutines*perG), total, "потерянные RecordRequest")
	assert.Equal(t, allowed.Load(), retried, "retried разошёлся с числом разрешений")

	// Верхняя граница расхода: каждое платное списание прошло проверку
	// (r+1)*100 <= percent*(t+1) при t <= total, плюс не более
	// budgetMinRequest-1 бесплатных, пока окно было холодным (каждому
	// AllowRetry предшествует RecordRequest той же горутины, так что до
	// десятого запроса успевает пройти не больше девяти проверок).
	maxRetried := percent*(total+1)/100 + budgetMinRequest - 1
	assert.LessOrEqual(t, retried, maxRetried)
}
