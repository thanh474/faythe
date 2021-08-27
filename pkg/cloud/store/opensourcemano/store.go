// Copyright (c) 2021 Manh Vu Duc <manhvd.hust@gmail.com>
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

package opensourcemano

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	etcdv3 "go.etcd.io/etcd/clientv3"

	"github.com/vCloud-DFTBA/faythe/pkg/cluster"
	"github.com/vCloud-DFTBA/faythe/pkg/common"
	"github.com/vCloud-DFTBA/faythe/pkg/exporter"
	"github.com/vCloud-DFTBA/faythe/pkg/model"
)

// Store supports get and set cloud information
type Store struct {
	mtx     sync.RWMutex
	etcdcli *common.Etcd
	clouds  map[string]model.OpenSourceMano
}

// Get returns cloud information
func (s *Store) Get(key string) (model.OpenSourceMano, bool) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()

	value, ok := s.clouds[key]
	if !ok {
		// Try to get Cloud provider info from Etcd
		r, err := s.etcdcli.DoGet(common.Path(model.DefaultCloudPrefix, key))
		if err != nil || len(r.Kvs) != 1 {
			return value, ok
		}
		cloud := model.OpenSourceMano{}
		if err := json.Unmarshal(r.Kvs[0].Value, &cloud); err != nil {
			return value, ok
		}
		if cloud.Provider == model.ManoType {
			s.Set(key, cloud)
			value = cloud
			ok = true
		}
	}
	return value, ok
}

// Set adds item to store
func (s *Store) Set(key string, value model.OpenSourceMano) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	s.clouds[key] = value
	exporter.ReportNumberOfClouds(cluster.GetID(), model.ManoType, 1)
}

// Delete removes an item from store
func (s *Store) Delete(key string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	delete(s.clouds, key)
	exporter.ReportNumberOfClouds(cluster.GetID(), model.ManoType, -1)
}

var s *Store

// InitStore creates a new store
func InitStore(e *common.Etcd) {
	s = &Store{
		etcdcli: e,
		clouds:  map[string]model.OpenSourceMano{},
	}
}

// Load retrieves cloud information from etcd
func Load() error {
	r, err := s.etcdcli.DoGet(model.DefaultCloudPrefix, etcdv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("error getting list of clouds from etcd")
	}

	for _, c := range r.Kvs {
		cID := strings.Split(string(c.Key), "/")[2]
		cloud := model.OpenSourceMano{}
		if err := json.Unmarshal(c.Value, &cloud); err != nil {
			return fmt.Errorf("error unmarshling cloud information")
		}
		if cloud.Provider == model.ManoType {
			s.Set(cID, cloud)
		}		
	}
	return nil
}

// Get returns store instance
func Get() *Store {
	return s
}