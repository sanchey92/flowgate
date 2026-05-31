package passive

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestState_String(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half-open"},
		{State(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.String())
		})
	}
}

func TestNewBreaker_AppliesDefaults(t *testing.T) {
	b := NewBreaker(&Config{})

	assert.Equal(t, int64(defaultErrorThreshold), b.threshold)
	assert.Equal(t, defaultRecoveryInterval, b.recovery)
	assert.Equal(t, defaultWindow/windowBuckets, b.window.bucketWidth)
}

func TestBreaker_InitialStateClosed(t *testing.T) {
	b := newTestBreaker(3, time.Minute)

	assert.Equal(t, StateClosed, b.CurrentState())
	assert.True(t, b.Allow(), "закрытый брейкер должен пропускать трафик")
}

func TestBreaker_SuccessesNeverOpen(t *testing.T) {
	b := newTestBreaker(3, time.Minute)

	for i := 0; i < 20; i++ {
		b.Observe(false)
	}

	assert.Equal(t, StateClosed, b.CurrentState())
	assert.True(t, b.Allow())
}

func TestBreaker_StaysClosedBelowThreshold(t *testing.T) {
	b := newTestBreaker(3, time.Minute)

	// Две ошибки вперемешку с успехами — успехи не сбрасывают счётчик окна,
	// но и порог в 3 ошибки ещё не достигнут.
	b.Observe(true)
	b.Observe(false)
	b.Observe(true)

	assert.Equal(t, StateClosed, b.CurrentState())
	assert.True(t, b.Allow())
}

func TestBreaker_OpensAtThreshold(t *testing.T) {
	b := newTestBreaker(3, time.Minute)

	b.Observe(true)
	b.Observe(true)
	require.Equal(t, StateClosed, b.CurrentState(), "до порога остаётся закрытым")

	b.Observe(true)
	assert.Equal(t, StateOpen, b.CurrentState(), "на пороге размыкается")
	assert.False(t, b.Allow(), "сразу после размыкания трафик блокируется до окончания recovery")
}

func TestBreaker_OpenTransitionsToHalfOpenAfterRecovery(t *testing.T) {
	b := newTestBreaker(1, time.Minute)

	b.Observe(true)
	require.Equal(t, StateOpen, b.CurrentState())
	require.False(t, b.Allow(), "recovery ещё не истёк")

	// Детерминированно «перематываем» время: recovery считается истёкшим.
	elapseRecovery(b)

	assert.True(t, b.Allow(), "по истечении recovery пропускается пробный запрос")
	assert.Equal(t, StateHalfOpen, b.CurrentState())
}

func TestBreaker_HalfOpenAllowsSingleProbe(t *testing.T) {
	b := newTestBreaker(1, time.Minute)
	driveToHalfOpen(t, b)

	// Первый пробный запрос уже «израсходован» внутри driveToHalfOpen,
	// поэтому следующий в пределах recovery должен быть заблокирован.
	assert.False(t, b.Allow(), "в half-open допускается лишь один пробник за recovery")
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	b := newTestBreaker(1, time.Minute)
	driveToHalfOpen(t, b)

	openedBefore := b.openedAt.Load()
	b.Observe(true)

	assert.Equal(t, StateOpen, b.CurrentState(), "провал пробника возвращает в open")
	assert.GreaterOrEqual(t, b.openedAt.Load(), openedBefore, "таймер recovery перезапускается")
	assert.False(t, b.Allow())
}

func TestBreaker_HalfOpenSuccessClosesAndResetsWindow(t *testing.T) {
	b := newTestBreaker(2, time.Minute)

	// Размыкаем брейкер двумя ошибками.
	b.Observe(true)
	b.Observe(true)
	require.Equal(t, StateOpen, b.CurrentState())

	elapseRecovery(b)
	require.True(t, b.Allow())
	require.Equal(t, StateHalfOpen, b.CurrentState())

	// Успешный пробник закрывает брейкер.
	b.Observe(false)
	require.Equal(t, StateClosed, b.CurrentState())

	// Окно должно быть сброшено: одной новой ошибки недостаточно для размыкания.
	b.Observe(true)
	assert.Equal(t, StateClosed, b.CurrentState(), "счётчик ошибок должен был обнулиться")

	// А вот второй ошибки уже хватает, чтобы снова достичь порога.
	b.Observe(true)
	assert.Equal(t, StateOpen, b.CurrentState())
}

func TestBreaker_ObserveOpenStateIsNoop(t *testing.T) {
	b := newTestBreaker(1, time.Minute)

	b.Observe(true)
	require.Equal(t, StateOpen, b.CurrentState())
	openedAt := b.openedAt.Load()

	// В состоянии open Observe не меняет ни состояние, ни таймер.
	b.Observe(true)
	b.Observe(false)

	assert.Equal(t, StateOpen, b.CurrentState())
	assert.Equal(t, openedAt, b.openedAt.Load())
}

func TestBreaker_Concurrent(t *testing.T) {
	b := newTestBreaker(50, time.Millisecond)

	const (
		workers      = 16
		opsPerWorker = 2000
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				// Чередуем успехи и ошибки, параллельно опрашивая Allow,
				// чтобы под -race поймать гонки на atomic-полях и окне.
				b.Observe(j%3 == 0)
				_ = b.Allow()
			}
		}(i)
	}
	wg.Wait()

	// Состояние должно остаться валидным независимо от исхода гонок.
	assert.Contains(t, []State{StateClosed, StateOpen, StateHalfOpen}, b.CurrentState())
}

func TestBreaker_Ready_ClosedStaysPure(t *testing.T) {
	b := newTestBreaker(3, time.Minute)

	assert.True(t, b.Ready(), "закрытый брейкер всегда готов")
	assert.Equal(t, StateClosed, b.CurrentState(), "Ready не меняет состояние")
}

func TestBreaker_Ready_OpenGatesOnRecovery(t *testing.T) {
	b := newTestBreaker(1, time.Minute)
	b.Observe(true)
	require.Equal(t, StateOpen, b.CurrentState())

	assert.False(t, b.Ready(), "до истечения recovery — не готов")

	elapseRecovery(b)
	assert.True(t, b.Ready(), "после recovery — готов принять пробник")
	assert.Equal(t, StateOpen, b.CurrentState(),
		"переход в half-open делает Allow, а не Ready")
}

func TestBreaker_Ready_DoesNotConsumeHalfOpenProbe(t *testing.T) {
	b := newTestBreaker(3, time.Minute)
	driveToHalfOpen(t, b) // half-open, первый пробник уже израсходован

	assert.False(t, b.Ready(), "пробник только что занят — слота нет")

	// Возвращаем слот и убеждаемся, что многократный Ready его не расходует.
	b.lastProbe.Store(nowNanos() - int64(b.recovery) - 1)
	assert.True(t, b.Ready())
	assert.True(t, b.Ready(), "повторный Ready не «съедает» пробник")
	assert.Equal(t, StateHalfOpen, b.CurrentState())

	// А Allow расходует: ровно один проходит, дальше снова не готов.
	require.True(t, b.Allow())
	assert.False(t, b.Ready())
}

// newTestBreaker собирает брейкер с большим окном, чтобы все ошибки в тесте
// заведомо попадали в одно временное окно.
func newTestBreaker(threshold int, recovery time.Duration) *Breaker {
	return NewBreaker(&Config{
		ErrorThreshold:   threshold,
		Window:           10 * time.Second,
		RecoveryInterval: recovery,
	})
}

// elapseRecovery детерминированно сдвигает момент размыкания в прошлое так,
// будто интервал recovery уже истёк — это избавляет тесты от реальных sleep.
func elapseRecovery(b *Breaker) {
	b.openedAt.Store(nowNanos() - int64(b.recovery) - 1)
}

// driveToHalfOpen размыкает брейкер (накопив ошибки до порога), переводит его
// в half-open по истечении recovery и расходует первый пробный запрос.
func driveToHalfOpen(t *testing.T, b *Breaker) {
	t.Helper()
	for i := int64(0); i < b.threshold; i++ {
		b.Observe(true)
	}
	require.Equal(t, StateOpen, b.CurrentState(), "брейкер должен разомкнуться по достижении порога")
	elapseRecovery(b)
	require.True(t, b.Allow(), "первый пробник после recovery должен пройти")
	require.Equal(t, StateHalfOpen, b.CurrentState())
}
