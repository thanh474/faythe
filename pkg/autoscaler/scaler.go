// Copyright (c) 2019 Kien Nguyen-Tuan <kiennt2609@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package autoscaler

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/avast/retry-go"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"go.etcd.io/etcd/clientv3/concurrency"

	"github.com/vCloud-DFTBA/faythe/pkg/metrics"
	"github.com/vCloud-DFTBA/faythe/pkg/model"
)

const (
	httpTimeout = time.Second * 15
)

// Scaler does metric polling and executes scale actions.
type Scaler struct {
	model.Scaler
	alert      *alert
	logger     log.Logger
	mtx        sync.RWMutex
	done       chan struct{}
	terminated chan struct{}
	backend    metrics.Backend
	dlock      concurrency.Mutex
}

func newScaler(l log.Logger, data []byte, b metrics.Backend) *Scaler {
	s := &Scaler{
		logger:     l,
		done:       make(chan struct{}),
		terminated: make(chan struct{}),
		backend:    b,
	}
	_ = json.Unmarshal(data, s)
	if s.Alert == nil {
		s.Alert = &model.Alert{}
	}
	s.alert = &alert{state: s.Alert}
	return s
}

func (s *Scaler) stop() {
	level.Debug(s.logger).Log("msg", "Scaler is stopping", "id", s.ID)
	close(s.done)
	<-s.terminated
	level.Debug(s.logger).Log("msg", "Scaler is stopped", "id", s.ID)
}

func (s *Scaler) run(ctx context.Context, wg *sync.WaitGroup) {
	interval, _ := time.ParseDuration(s.Interval)
	duration, _ := time.ParseDuration(s.Duration)
	cooldown, _ := time.ParseDuration(s.Cooldown)
	ticker := time.NewTicker(interval)
	defer func() {
		ticker.Stop()
		wg.Done()
		close(s.terminated)
	}()

	for {
		select {
		case <-s.done:
			return
		default:
			select {
			case <-s.done:
				return
			case <-ticker.C:
				if !s.Active {
					continue
				}
				result, err := s.backend.QueryInstant(ctx, s.Query, time.Now())
				if err != nil {
					level.Error(s.logger).Log("msg", "Executing query failed, skip current interval",
						"query", s.Query, "err", err)
					continue
				}
				level.Debug(s.logger).Log("msg", "Executing query success",
					"query", s.Query)
				s.mtx.Lock()
				if len(result) == 0 {
					s.alert.reset()
					continue
				}
				if !s.alert.isActive() {
					s.alert.start()
				}
				if s.alert.shouldFire(duration) && !s.alert.isCoolingDown(cooldown) {
					s.do()
				}
				s.mtx.Unlock()
			}
		}
	}
}

// do simply creates and executes a POST request
func (s *Scaler) do() {
	var (
		wg  sync.WaitGroup
		tr  *http.Transport
		cli *http.Client
	)
	tr = &http.Transport{}
	cli = &http.Client{
		Transport: tr,
		Timeout:   httpTimeout,
	}

	for _, a := range s.Actions {
		go func(url string) {
			wg.Add(1)
			delay, _ := time.ParseDuration(a.Delay)
			err := retry.Do(
				func() error {
					// TODO(kiennt): Check kind of action url -> Authen or not?
					req, err := http.NewRequest(a.Method, url, nil)
					if err != nil {
						return err
					}
					resp, err := cli.Do(req)
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					return nil
				},
				retry.DelayType(func(n uint, config *retry.Config) time.Duration {
					var f retry.DelayTypeFunc
					switch a.DelayType {
					case "fixed":
						f = retry.FixedDelay
					case "backoff":
						f = retry.BackOffDelay
					}
					return f(n, config)
				}),
				retry.Attempts(a.Attempts),
				retry.Delay(delay),
				retry.RetryIf(func(err error) bool {
					if err, ok := err.(net.Error); ok && err.Timeout() {
						return true
					}
					return false
				}),
			)
			if err != nil {
				level.Error(s.logger).Log("msg", "Error doing scale action", "url", a.URL.String(), "err", err)
				return
			}
			level.Info(s.logger).Log("msg", "Sending request", "id", s.ID,
				"url", url, "method", a.Method)
			s.alert.fire(time.Now())
			defer wg.Done()
		}(string(a.URL))
	}
	// Wait until all actions were performed
	wg.Wait()
}

type alert struct {
	state   *model.Alert
	cooling bool
}

func (a *alert) shouldFire(duration time.Duration) bool {
	return a.state.Active && time.Now().Sub(a.state.StartedAt) >= duration
}

func (a *alert) isCoolingDown(cooldown time.Duration) bool {
	a.cooling = time.Now().Sub(a.state.FiredAt) <= cooldown
	return a.cooling
}

func (a *alert) start() {
	a.state.StartedAt = time.Now()
	a.state.Active = true
}

func (a *alert) fire(firedAt time.Time) {
	if a.state.FiredAt.IsZero() || !a.cooling {
		a.state.FiredAt = firedAt
	}
}

func (a *alert) reset() {
	a.state.StartedAt = time.Time{}
	a.state.Active = false
	a.state.FiredAt = time.Time{}
}

func (a *alert) isActive() bool {
	return a.state.Active
}