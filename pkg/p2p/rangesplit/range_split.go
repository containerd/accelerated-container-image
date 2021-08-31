/*
   Copyright The Accelerated Container Image Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package rangesplit

import (
	"errors"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"
)

// RangeSplit utility for split range into segments
type RangeSplit struct {
	offset int64
	step   int
	size   int64
}

// RangeSegment segment information for split range
type RangeSegment struct {
	Index  int64
	Offset int64
	Count  int
}

// NewRangeSplit is creator for RangeSplit
func NewRangeSplit(offset int64, step int, size int64, maxsize int64) RangeSplit {
	if (step & (step - 1)) > 0 {
		panic(errors.New("step must be power of 2"))
	}
	return RangeSplit{offset, step, util.Min64(offset+size, maxsize)}
}

// AlignDown will align down the x by align
func AlignDown(x int64, align int64) int64 {
	return x / align * align
}

// AllParts provides a channel as iterable object to range segments
func (r RangeSplit) AllParts() chan RangeSegment {
	ch := make(chan RangeSegment)
	go func() {
		for i := AlignDown(r.offset, int64(r.step)); i < r.size; i += int64(r.step) {
			absOffset := util.Max64(i, r.offset)
			seg := RangeSegment{Index: i, Offset: absOffset - i}
			seg.Count = int(util.Min64(i+int64(r.step), r.size) - absOffset)
			if seg.Count > 0 {
				ch <- seg
			}
		}
		close(ch)
	}()
	return ch
}
