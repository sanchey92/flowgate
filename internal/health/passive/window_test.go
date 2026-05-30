package passive

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Окно из 10 секунд, разбитое на 10 корзин по 1 секунде — каждая корзина
// соответствует одной секунде wall-clock, что даёт «круглые» epoch-номера.
const (
	testWindow  = 10 * time.Second
	testBuckets = 10
)

func TestNewWindowCounter_BucketWidth(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)

	assert.Len(t, w.buckets, testBuckets)
	assert.Len(t, w.epochs, testBuckets)
	assert.Equal(t, testWindow/testBuckets, w.bucketWidth)
}

func TestNewWindowCounter_NonPositiveCountFallsBackToDefault(t *testing.T) {
	tests := []struct {
		name string
		n    int
	}{
		{name: "zero", n: 0},
		{name: "negative", n: -3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWindowCounter(testWindow, tt.n)
			assert.Len(t, w.buckets, windowBuckets)
			assert.Equal(t, testWindow/windowBuckets, w.bucketWidth)
		})
	}
}

func TestWindowCounter_AddAccumulatesWithinSameBucket(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	// base попадает ровно на границу секунды → epoch = 1000, idx = 0.
	base := time.Unix(1000, 0)

	assert.Equal(t, int64(1), w.Add(base))
	assert.Equal(t, int64(2), w.Add(base))
	// Полсекунды позже — всё ещё та же корзина (ширина 1с).
	assert.Equal(t, int64(3), w.Add(base.Add(500*time.Millisecond)))
}

func TestWindowCounter_AddAccumulatesAcrossBucketsInsideWindow(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	base := time.Unix(1000, 0)

	// Три разные секунды (epoch 1000, 1001, 1002) — все внутри окна.
	assert.Equal(t, int64(1), w.Add(base))
	assert.Equal(t, int64(2), w.Add(base.Add(1*time.Second)))
	assert.Equal(t, int64(3), w.Add(base.Add(2*time.Second)))
}

func TestWindowCounter_SlidingWindowStaysBounded(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	base := time.Unix(1000, 0)

	// Заполняем все 10 корзин по одному событию в каждой секунде.
	var got int64
	for i := 0; i < testBuckets; i++ {
		got = w.Add(base.Add(time.Duration(i) * time.Second))
	}
	require.Equal(t, int64(testBuckets), got, "полное окно должно содержать ровно 10 событий")

	// Следующая секунда вытесняет самую старую корзину: сумма остаётся 10.
	got = w.Add(base.Add(time.Duration(testBuckets) * time.Second))
	assert.Equal(t, int64(testBuckets), got, "окно скользит, а не растёт")
}

func TestWindowCounter_ExpiresStaleBucketOnIndexReuse(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	base := time.Unix(1000, 0)

	// epoch 1000 → idx 0.
	require.Equal(t, int64(1), w.Add(base))

	// epoch 1010 → тоже idx 0 (1010 % 10 == 0), но корзина протухла:
	// старое значение обнуляется, а не суммируется.
	got := w.Add(base.Add(10 * time.Second))
	assert.Equal(t, int64(1), got, "переиспользованная корзина должна обнулиться")
}

func TestWindowCounter_ExcludesStaleBucketWithoutOverwrite(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	base := time.Unix(1000, 0)

	// epoch 1000 → idx 0.
	require.Equal(t, int64(1), w.Add(base))
	// epoch 1005 → idx 5.
	require.Equal(t, int64(2), w.Add(base.Add(5*time.Second)))

	// epoch 1011 → idx 1. minEpoch = 1011-9 = 1002, поэтому корзина idx 0
	// (epoch 1000) выпадает из окна по проверке epoch, хотя физически не перезаписана.
	got := w.Add(base.Add(11 * time.Second))
	assert.Equal(t, int64(2), got, "устаревшая корзина исключается по epoch, а не только перезаписью")
}

func TestWindowCounter_HandlesNegativeEpoch(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	// Время до Unix-эпохи даёт отрицательный epoch → отрицательный остаток от деления,
	// который должен корректно нормализоваться в валидный индекс корзины.
	before := time.Unix(-1000, 0)

	assert.Equal(t, int64(1), w.Add(before))
	assert.Equal(t, int64(2), w.Add(before))
}

func TestWindowCounter_Reset(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	base := time.Unix(1000, 0)

	w.Add(base)
	w.Add(base.Add(1 * time.Second))
	w.Add(base.Add(2 * time.Second))

	w.Reset()

	// После сброса счёт начинается заново даже для тех же временных меток.
	assert.Equal(t, int64(1), w.Add(base))
}

func TestWindowCounter_AddConcurrent(t *testing.T) {
	w := newWindowCounter(testWindow, testBuckets)
	// Все горутины пишут в одну и ту же метку времени → одна корзина,
	// поэтому итоговая сумма должна быть точной (проверяется с -race).
	at := time.Unix(1000, 0)

	const (
		workers      = 16
		perGoroutine = 1000
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				w.Add(at)
			}
		}()
	}
	wg.Wait()

	// Контрольное добавление должно увидеть все предыдущие инкременты.
	got := w.Add(at)
	assert.Equal(t, int64(workers*perGoroutine+1), got, "ни один инкремент не должен потеряться")
}
