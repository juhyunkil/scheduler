// Copyright 2016 Google Inc. All Rights Reserved.
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

package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

var processorLock = &sync.Mutex{}

func monitorUnscheduledPods(done chan struct{}, wg *sync.WaitGroup) { //새롭게 들어온 파드 감시
	pods, errc := watchUnscheduledPods() //새롭게 들어온 파드 얻음
	for {
		select {
		case err := <-errc:
			log.Println(err)
		case pod := <-pods:
			processorLock.Lock()
			time.Sleep(2 * time.Second)
			err := schedulePod(&pod) //새롭게 들어온 파드 스케줄링
			if err != nil {
				log.Println(err)
			}
			processorLock.Unlock()
		case <-done:
			wg.Done() //대기중인 고루틴의 수행이 종료되는 것을 알려줌
			log.Println("Stopped scheduler.")
			return
		}
	}
}

func reconcileUnscheduledPods(interval int, done chan struct{}, wg *sync.WaitGroup) { //스케줄링 실패한 파드 감시
	for {
		select {
		case <-time.After(time.Duration(interval) * time.Second): //주기적으로 재 스케줄링
			err := schedulePods()
			if err != nil {
				log.Println(err)
			}
		case <-done:
			wg.Done()
			log.Println("Stopped reconciliation loop.")
			return
		}
	}
}

func schedulePod(pod *Pod) error { //파드 스케줄링
	nodes, err := fit(pod) //filtering
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("Unable to schedule pod (%s) failed to fit in any node", pod.Metadata.Name)
	}
	node, err := bestPrice(nodes) //scoring
	if err != nil {
		return err
	}
	err = bind(pod, node) //binding
	if err != nil {
		return err
	}
	return nil
}

func schedulePods() error { //called by reconcileUnscheduledPods
	processorLock.Lock()
	defer processorLock.Unlock()
	pods, err := getUnscheduledPods()
	if err != nil {
		return err
	}
	for _, pod := range pods { //스케줄링 대기중인 파드들 하나씩 스케줄링
		err := schedulePod(pod)
		if err != nil {
			log.Println(err)
		}
	}
	return nil
}
