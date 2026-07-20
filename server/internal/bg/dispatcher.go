// Package bg is the goroutine-based background job runner that replaces Plane's
// Celery workers. Handlers submit fire-and-forget jobs (the analog of Celery's
// task.delay()); a bounded pool of worker goroutines runs them off the request
// path, so the HTTP response returns immediately.
//
// This is intentionally in-process (no broker): the Go port runs as a single
// server, so a channel + worker pool gives the same "enqueue now, run soon"
// semantics Celery's .delay() provided, without RabbitMQ/Redis.
package bg

import (
	"context"
	"log"
	"sync"
	"time"
)

// Job is a unit of background work. It receives a context that is cancelled on
// dispatcher shutdown (after a drain grace period).
type Job func(ctx context.Context)

// Dispatcher owns the worker pool and job queue.
type Dispatcher struct {
	jobs   chan Job
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
	closed chan struct{}
	once   sync.Once
}

// New starts `workers` goroutines consuming a buffered queue of `buffer` jobs.
func New(workers, buffer int) *Dispatcher {
	if workers < 1 {
		workers = 1
	}
	if buffer < 1 {
		buffer = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{
		jobs:   make(chan Job, buffer),
		ctx:    ctx,
		cancel: cancel,
		closed: make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	return d
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for job := range d.jobs {
		d.run(job)
	}
}

// run executes one job with panic recovery, so a bad job never takes down a
// worker goroutine (Celery isolates task failures the same way).
func (d *Dispatcher) run(job Job) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("bg: job panicked: %v", r)
		}
	}()
	job(d.ctx)
}

// Submit enqueues a job. It never blocks the request path: if the queue is
// full, the job is dropped with a log line (Celery would also shed load under
// broker backpressure). After Shutdown, submits are ignored.
func (d *Dispatcher) Submit(job Job) {
	if job == nil {
		return
	}
	select {
	case <-d.closed:
		return
	default:
	}
	select {
	case d.jobs <- job:
	case <-d.closed:
	default:
		log.Printf("bg: queue full, dropping job")
	}
}

// Shutdown stops accepting jobs, drains what's queued (up to the grace period),
// then cancels in-flight job contexts and waits for workers to exit.
func (d *Dispatcher) Shutdown(grace time.Duration) {
	d.once.Do(func() {
		close(d.closed)
		close(d.jobs)
		done := make(chan struct{})
		go func() { d.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(grace):
			d.cancel()
			<-done
		}
		d.cancel()
	})
}
