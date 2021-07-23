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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/rest"
)

//클러스터의 노드 반환
func getNodes() (*NodeList, error) {
	var nodeList NodeList

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{ //client구조체 생성
		Transport: tr,
	}

	request := &http.Request{ //request구조체 생성
		Header: make(http.Header),
		Method: http.MethodGet,
		URL: &url.URL{
			Host:   apiHost,
			Path:   nodesEndpoint,
			Scheme: "https",
		},
	}

	host_config, _ := rest.InClusterConfig()
	token := host_config.BearerToken
	request.Header.Set("Authorization", "Bearer "+token) //헤더추가
	request.Header.Set("Accept", "application/json, */*")

	resp, err := client.Do(request)
	if err != nil {
		return nil, err
	}

	err = json.NewDecoder(resp.Body).Decode(&nodeList) //표준 입력에서 들어온 데이터를 스트림 방식으로 디코딩
	if err != nil {
		return nil, err
	}

	return &nodeList, nil
}

//클러스터의 파드 반환
func getPods() (*PodList, error) {
	var podList PodList

	v := url.Values{}
	v.Add("fieldSelector", "status.phase=Running")
	v.Add("fieldSelector", "status.phase=Pending")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{
		Transport: tr,
	}

	request := &http.Request{
		Header: make(http.Header),
		Method: http.MethodGet,
		URL: &url.URL{
			Host:     apiHost,
			Path:     podsEndpoint,
			RawQuery: v.Encode(),
			Scheme:   "https",
		},
	}

	host_config, _ := rest.InClusterConfig()
	token := host_config.BearerToken
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json, */*")

	resp, err := client.Do(request)

	if err != nil {
		return nil, err
	}
	err = json.NewDecoder(resp.Body).Decode(&podList) //json형식의 resp.body를 go value인 podList로 디코딩
	if err != nil {
		return nil, err
	}
	return &podList, nil
}

type ResourceUsage struct {
	CPU int
}

//노드 필터링
func fit(pod *Pod) ([]Node, error) {
	fmt.Println("--called filtering statge--")

	nodeList, err := getNodes() //전체 노드리스트
	if err != nil {
		return nil, err
	}

	podList, err := getPods() //전체 파드리스트
	if err != nil {
		return nil, err
	}

	if !check_needed_resources(pod, &nodeList) {
		return nil, err
	}

	//각 노드의 리소스 사용량을 저장하기 위한 resourseUsage맵
	resourceUsage := make(map[string]*ResourceUsage) //key=string,value=ResourceUsage struct
	for _, node := range nodeList.Items {
		resourceUsage[node.Metadata.Name] = &ResourceUsage{}
	}

	//각 노드의 자원 사용량 계산, 현재는 cpu만 고려
	for _, p := range podList.Items { //파드 전체 검사
		if p.Spec.NodeName == "" { //배치 안된 파드는 건너뜀
			continue
		}
		for _, c := range p.Spec.Containers { //파드의 컨테이너 검사
			if strings.HasSuffix(c.Resources.Requests["cpu"], "m") { //파드 컨테이너의 cpu요구사항이 m으로 끝나는지 확인
				milliCores := strings.TrimSuffix(c.Resources.Requests["cpu"], "m") //맨 끝의 접미사 m제거
				cores, err := strconv.Atoi(milliCores)                             //문자열->숫자열
				if err != nil {
					return nil, err
				}
				ru := resourceUsage[p.Spec.NodeName] //파드의 cpu사용량을 얻어 해당 노드의 리소스 사용량에 더함
				ru.CPU += cores
			}
		}
	}

	var nodes []Node
	fitFailures := make([]string, 0)

	//스케줄링할 파드의 자원 요구량 계산, 현재는 cpu만 고려
	var spaceRequired int
	for _, c := range pod.Spec.Containers {
		if strings.HasSuffix(c.Resources.Requests["cpu"], "m") {
			milliCores := strings.TrimSuffix(c.Resources.Requests["cpu"], "m")
			cores, err := strconv.Atoi(milliCores)
			if err != nil {
				return nil, err
			}
			spaceRequired += cores
		}
	}

	//각 노드를 순회하며 자원을 충족하는 노드 필터링
	for _, node := range nodeList.Items {
		var allocatableCores int
		var err error
		if strings.HasSuffix(node.Status.Allocatable["cpu"], "m") { //"cpu":"125m"
			milliCores := strings.TrimSuffix(node.Status.Allocatable["cpu"], "m")
			allocatableCores, err = strconv.Atoi(milliCores)
			if err != nil {
				return nil, err
			}
		} else { //"cpu":"8"
			cpu := node.Status.Allocatable["cpu"]
			cpuFloat, err := strconv.ParseFloat(cpu, 32)
			if err != nil {
				return nil, err
			}
			allocatableCores = int(cpuFloat * 1000)
		}

		freeSpace := (allocatableCores - resourceUsage[node.Metadata.Name].CPU) //노드의 자원 가용량
		if freeSpace < spaceRequired {                                          //가용량 < 요구량 -> 배치불가노드
			m := fmt.Sprintf("fit failure on node (%s): Insufficient CPU", node.Metadata.Name)
			fitFailures = append(fitFailures, m)
			continue
		}
		nodes = append(nodes, node) //배치 가능 노드로 설정
	}

	//배치 가능한 노드가 하나도 없을 때 이벤트 기록
	if len(nodes) == 0 {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		event := Event{
			Count:          1,
			Message:        fmt.Sprintf("pod (%s) failed to fit in any node\n%s", pod.Metadata.Name, strings.Join(fitFailures, "\n")),
			Metadata:       Metadata{GenerateName: pod.Metadata.Name + "-"},
			Reason:         "FailedScheduling",
			LastTimestamp:  timestamp,
			FirstTimestamp: timestamp,
			Type:           "Warning",
			Source:         EventSource{Component: "custom-gpu-scheduler"},
			InvolvedObject: ObjectReference{
				Kind:      "Pod",
				Name:      pod.Metadata.Name,
				Namespace: "default",
				Uid:       pod.Metadata.Uid,
			},
		}

		postEvent(event)
	}

	return nodes, nil
}

func check_gpu_exists(pod *Pod, nodeList *NodeList) bool { //노드에 gpu 존재 확인
	log.Println("called function check_gpu_exists")

}
