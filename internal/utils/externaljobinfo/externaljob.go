// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package externaljobinfo

import (
	"bytes"
	"encoding/json"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

type ExternalJobInfo struct {
	Pods []string `json:"pods"`
	// PodUIDs associates each namespace/name in Pods with its immutable identity.
	// Keeping Pods unchanged lets older bridge versions read these comments.
	// Missing UIDs retain name-only matching for jobs created before an upgrade.
	PodUIDs map[string]types.UID `json:"podUIDs,omitempty"`
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
