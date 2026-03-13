package memwatcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// countingNotifier подсчитывает вызовы Notify() и может возвращать заданную ошибку.
type countingNotifier struct {
	calls atomic.Int32
	err   error
}

func (c *countingNotifier) Notify(_ context.Context, _ DumpNotification) error {
	c.calls.Add(1)
	return c.err
}

// slowNotifier имитирует медленный HTTP вызов — спит delay перед возвратом.
// Используется для проверки параллельного выполнения.
type slowNotifier struct {
	delay time.Duration
	err   error
}

func (s *slowNotifier) Notify(ctx context.Context, _ DumpNotification) error {
	select {
	case <-time.After(s.delay):
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestNewMultiNotifier_AllNil проверяет что при передаче только nil-значений
// возвращается NoopNotifier (не MultiNotifier с пустым слайсом).
func TestNewMultiNotifier_AllNil(t *testing.T) {
	n := NewMultiNotifier(nil, nil, nil)
	if _, ok := n.(NoopNotifier); !ok {
		t.Errorf("NewMultiNotifier(nil, nil, nil) should return NoopNotifier, got %T", n)
	}
}

// TestNewMultiNotifier_NoArgs проверяет вызов без аргументов.
func TestNewMultiNotifier_NoArgs(t *testing.T) {
	n := NewMultiNotifier()
	if _, ok := n.(NoopNotifier); !ok {
		t.Errorf("NewMultiNotifier() should return NoopNotifier, got %T", n)
	}
}

// TestNewMultiNotifier_FiltersNils проверяет что nil-значения фильтруются,
// а ненулевые notifier'ы сохраняются.
func TestNewMultiNotifier_FiltersNils(t *testing.T) {
	real := &countingNotifier{}
	n := NewMultiNotifier(nil, real, nil)

	mn, ok := n.(*MultiNotifier)
	if !ok {
		t.Fatalf("expected *MultiNotifier, got %T", n)
	}
	if len(mn.notifiers) != 1 {
		t.Errorf("expected 1 notifier after filtering nils, got %d", len(mn.notifiers))
	}
}

// TestMultiNotifier_AllSuccess проверяет что все notifier'ы вызываются и nil возвращается.
func TestMultiNotifier_AllSuccess(t *testing.T) {
	a := &countingNotifier{}
	b := &countingNotifier{}
	n := NewMultiNotifier(a, b)

	err := n.Notify(context.Background(), dumpNotif)
	if err != nil {
		t.Fatalf("Notify() = %v, want nil", err)
	}
	if a.calls.Load() != 1 {
		t.Errorf("notifier a called %d times, want 1", a.calls.Load())
	}
	if b.calls.Load() != 1 {
		t.Errorf("notifier b called %d times, want 1", b.calls.Load())
	}
}

// TestMultiNotifier_PartialError проверяет что ошибка одного notifier'а
// не блокирует остальных, и ошибка возвращается наружу.
func TestMultiNotifier_PartialError(t *testing.T) {
	errSentinel := errors.New("slack is down")
	failing := &countingNotifier{err: errSentinel}
	succeeding := &countingNotifier{}
	n := NewMultiNotifier(failing, succeeding)

	err := n.Notify(context.Background(), dumpNotif)
	if err == nil {
		t.Fatal("Notify() should return error when one notifier fails")
	}
	if !errors.Is(err, errSentinel) {
		t.Errorf("err does not wrap sentinel: %v", err)
	}
	// Успешный notifier должен был вызваться несмотря на ошибку другого.
	if succeeding.calls.Load() != 1 {
		t.Errorf("succeeding notifier not called")
	}
}

// TestMultiNotifier_AllErrors проверяет что errors.Join объединяет все ошибки.
func TestMultiNotifier_AllErrors(t *testing.T) {
	err1 := errors.New("err1")
	err2 := errors.New("err2")
	n := NewMultiNotifier(
		&countingNotifier{err: err1},
		&countingNotifier{err: err2},
	)

	combined := n.Notify(context.Background(), dumpNotif)
	if combined == nil {
		t.Fatal("Notify() should return combined error")
	}
	if !errors.Is(combined, err1) {
		t.Errorf("combined error doesn't wrap err1: %v", combined)
	}
	if !errors.Is(combined, err2) {
		t.Errorf("combined error doesn't wrap err2: %v", combined)
	}
}

// TestMultiNotifier_Parallel проверяет что notifier'ы вызываются параллельно:
// два notifier'а со sleep 100ms должны завершиться суммарно менее чем за 200ms.
func TestMultiNotifier_Parallel(t *testing.T) {
	delay := 100 * time.Millisecond
	n := NewMultiNotifier(
		&slowNotifier{delay: delay},
		&slowNotifier{delay: delay},
	)

	start := time.Now()
	if err := n.Notify(context.Background(), dumpNotif); err != nil {
		t.Fatalf("Notify() = %v", err)
	}
	elapsed := time.Since(start)

	// Параллельный вызов занимает ~100ms, последовательный — ~200ms.
	// Даём запас: порог 180ms чтобы избежать flakiness на нагруженных CI.
	if elapsed >= 180*time.Millisecond {
		t.Errorf("Notify() elapsed %v, expected < 180ms (parallel execution)", elapsed)
	}
}

// TestMultiNotifier_SingleNotifier проверяет что один notifier без обёртки тоже работает.
func TestMultiNotifier_SingleNotifier(t *testing.T) {
	c := &countingNotifier{}
	n := NewMultiNotifier(c)

	if err := n.Notify(context.Background(), dumpNotif); err != nil {
		t.Fatalf("Notify() = %v", err)
	}
	if c.calls.Load() != 1 {
		t.Errorf("notifier called %d times, want 1", c.calls.Load())
	}
}
