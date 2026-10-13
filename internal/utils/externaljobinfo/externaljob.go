// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package externaljobinfo

import (
	"bytes"
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

type ExternalJobInfo struct {
	// Pods contains namespace/name/uid identities, or legacy namespace/name keys.
	Pods []string `json:"pods"`
}

// MarshalJSON stores the identities in one string without repeating pod names.
// Older readers expecting an array reject this format instead of interpreting
// the UID as part of a pod name and canceling its allocation.
func (extInfo ExternalJobInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Pods string `json:"pods"`
	}{Pods: strings.Join(extInfo.Pods, ",")})
}

// UnmarshalJSON accepts compact identities and the earlier array/map formats.
func (extInfo *ExternalJobInfo) UnmarshalJSON(data []byte) error {
	var wire struct {
		Pods    json.RawMessage      `json:"pods"`
		PodUIDs map[string]types.UID `json:"podUIDs"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var pods []string
	if len(wire.Pods) > 0 && wire.Pods[0] == '"' {
		var joined string
		if err := json.Unmarshal(wire.Pods, &joined); err != nil {
			return err
		}
		pods = []string{}
		if joined != "" {
			pods = strings.Split(joined, ",")
		}
	} else if len(wire.Pods) > 0 {
		if err := json.Unmarshal(wire.Pods, &pods); err != nil {
			return err
		}
		for i, name := range pods {
			if uid := wire.PodUIDs[name]; uid != "" {
				pods[i] = name + "/" + string(uid)
			}
		}
	}
	extInfo.Pods = pods
	return nil
}

func PodKey(pod *corev1.Pod) string {
	name := pod.Namespace + "/" + pod.Name
	if pod.UID == "" {
		return name
	}
	return name + "/" + string(pod.UID)
}

func ParsePodKey(key string) corev1.ObjectReference {
	namespace, name, namespaced := strings.Cut(key, "/")
	if !namespaced {
		return corev1.ObjectReference{Name: key}
	}
	name, uid, _ := strings.Cut(name, "/")
	return corev1.ObjectReference{Namespace: namespace, Name: name, UID: types.UID(uid)}
}

func (extInfo *ExternalJobInfo) Equal(cmp ExternalJobInfo) bool {
	a, _ := json.Marshal(extInfo)
	b, _ := json.Marshal(cmp)
	return bytes.Equal(a, b)
}

func (podInfo *ExternalJobInfo) ToString() string {
	b, _ := json.Marshal(podInfo)
	return string(b)
}

func ParseIntoExternalJobInfo(str *string, out *ExternalJobInfo) error {
	data := ptr.Deref(str, "")
	return json.Unmarshal([]byte(data), out)
}
