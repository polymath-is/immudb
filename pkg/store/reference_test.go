/*
Copyright 2019-2020 vChain, Inc.

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

package store

import (
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/stretchr/testify/assert"
	"strconv"
	"testing"
)

func TestStoreReference(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	_, err := st.Set(schema.KeyValue{Key: []byte(`firstKey`), Value: []byte(`firstValue`)})

	assert.NoError(t, err)

	refOpts := &schema.ReferenceOptions{
		Reference: []byte(`myTag`),
		Key:       []byte(`firstKey`),
	}

	reference, err := st.Reference(refOpts)

	assert.NoError(t, err)
	assert.Exactly(t, reference.Index, uint64(1))
	assert.NotEmptyf(t, reference, "Should not be empty")

	firstItemRet, err := st.Get(schema.Key{Key: []byte(`myTag`)})

	assert.NoError(t, err)
	assert.NotEmptyf(t, firstItemRet, "Should not be empty")
	assert.Equal(t, firstItemRet.Value, []byte(`firstValue`), "Should have referenced item value")
}

func TestStore_GetReferenceWithKeyResolution(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	_, _ = st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item1`)})

	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag1`), Key: []byte(`aaa`)})

	tag3, err := st.GetReference(schema.Key{Key: []byte(`myTag1`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag3.Key)
	assert.Equal(t, []byte(`item1`), tag3.Value)
}

func TestStore_GetReferenceWithIndexResolution(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	idx1, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item1`)})

	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag1`), Index: idx1, Key: []byte(`aaa`)})

	tag3, err := st.GetReference(schema.Key{Key: []byte(`myTag1`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag3.Key)
	assert.Equal(t, []byte(`item1`), tag3.Value)
}

func TestStoreReferenceAsyncCommit(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	firstIndex, _ := st.Set(schema.KeyValue{Key: []byte(`firstKey`), Value: []byte(`firstValue`)})
	secondIndex, _ := st.Set(schema.KeyValue{Key: []byte(`secondKey`), Value: []byte(`secondValue`)})

	for n := uint64(0); n <= 64; n++ {
		tag := []byte(strconv.FormatUint(n, 10))
		var itemKey []byte
		if n%2 == 0 {
			itemKey = []byte(`firstKey`)
		} else {
			itemKey = []byte(`secondKey`)
		}
		refOpts := &schema.ReferenceOptions{
			Reference: tag,
			Key:       itemKey,
		}
		index, err := st.Reference(refOpts, WithAsyncCommit(true))
		assert.NoError(t, err, "n=%d", n)
		assert.Equal(t, n+2, index.Index, "n=%d", n)
	}

	st.Wait()

	for n := uint64(0); n <= 64; n++ {
		tag := []byte(strconv.FormatUint(n, 10))
		var itemKey []byte
		var itemVal []byte
		var index uint64
		if n%2 == 0 {
			itemKey = []byte(`firstKey`)
			itemVal = []byte(`firstValue`)
			index = firstIndex.Index
		} else {
			itemKey = []byte(`secondKey`)
			itemVal = []byte(`secondValue`)
			index = secondIndex.Index
		}
		item, err := st.Get(schema.Key{Key: tag})
		assert.NoError(t, err, "n=%d", n)
		assert.Equal(t, index, item.Index, "n=%d", n)
		assert.Equal(t, itemVal, item.Value, "n=%d", n)
		assert.Equal(t, itemKey, item.Key, "n=%d", n)
	}
}

func TestStoreMultipleReferenceOnSameKey(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	idx0, err := st.Set(schema.KeyValue{Key: []byte(`firstKey`), Value: []byte(`firstValue`)})
	idx1, err := st.Set(schema.KeyValue{Key: []byte(`firstKey`), Value: []byte(`secondValue`)})

	assert.NoError(t, err)

	refOpts1 := &schema.ReferenceOptions{
		Reference: []byte(`myTag1`),
		Key:       []byte(`firstKey`),
		Index:     idx0,
	}
	reference1, err := st.Reference(refOpts1)
	assert.NoError(t, err)
	assert.Exactly(t, uint64(2), reference1.Index)
	assert.NotEmptyf(t, reference1, "Should not be empty")

	refOpts2 := &schema.ReferenceOptions{
		Reference: []byte(`myTag2`),
		Key:       []byte(`firstKey`),
		Index:     idx0,
	}
	reference2, err := st.Reference(refOpts2)
	assert.NoError(t, err)
	assert.Exactly(t, uint64(3), reference2.Index)
	assert.NotEmptyf(t, reference2, "Should not be empty")

	refOpts3 := &schema.ReferenceOptions{
		Reference: []byte(`myTag3`),
		Key:       []byte(`firstKey`),
		Index:     idx1,
	}
	reference3, err := st.Reference(refOpts3)
	assert.NoError(t, err)
	assert.Exactly(t, uint64(4), reference3.Index)
	assert.NotEmptyf(t, reference3, "Should not be empty")

	firstTagRet, err := st.GetReference(schema.Key{Key: []byte(`myTag1`)})

	assert.NoError(t, err)
	assert.NotEmptyf(t, firstTagRet, "Should not be empty")
	assert.Equal(t, []byte(`firstValue`), firstTagRet.Value, "Should have referenced item value")

	secondTagRet, err := st.GetReference(schema.Key{Key: []byte(`myTag2`)})

	assert.NoError(t, err)
	assert.NotEmptyf(t, secondTagRet, "Should not be empty")
	assert.Equal(t, []byte(`firstValue`), secondTagRet.Value, "Should have referenced item value")

	thirdItemRet, err := st.Get(schema.Key{Key: []byte(`myTag3`)})

	assert.NoError(t, err)
	assert.NotEmptyf(t, thirdItemRet, "Should not be empty")
	assert.Equal(t, []byte(`secondValue`), thirdItemRet.Value, "Should have referenced item value")
}

func TestStoreIndexReference(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	idx1, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item1`)})
	idx2, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item2`)})
	st.Reference(&schema.ReferenceOptions{Key: []byte(`aaa`), Reference: []byte(`myTag1`), Index: idx1})
	st.Reference(&schema.ReferenceOptions{Key: []byte(`aaa`), Reference: []byte(`myTag2`), Index: idx2})

	tag1, err := st.GetReference(schema.Key{Key: []byte(`myTag1`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag1.Key)
	assert.Equal(t, []byte(`item1`), tag1.Value)

	tag2, err := st.Get(schema.Key{Key: []byte(`myTag2`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag2.Key)
	assert.Equal(t, []byte(`item2`), tag2.Value)

}

func TestStoreOnlyIndexReference(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	idx1, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item1`)})
	idx2, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item2`)})
	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag1`), Index: idx1})
	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag2`), Index: idx2})

	tag1, err := st.GetReference(schema.Key{Key: []byte(`myTag1`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag1.Key)
	assert.Equal(t, []byte(`item1`), tag1.Value)

	tag2, err := st.Get(schema.Key{Key: []byte(`myTag2`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag2.Key)
	assert.Equal(t, []byte(`item2`), tag2.Value)

}

func TestStore_GetOnReferenceOnSameKeyReturnsAlwaysLastValue(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	idx1, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item1`)})
	idx2, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item2`)})
	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag1`), Index: idx1})
	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag2`), Index: idx2})

	tag2, err := st.Get(schema.Key{Key: []byte(`myTag2`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag2.Key)
	assert.Equal(t, []byte(`item2`), tag2.Value)

	tag1b, err := st.Get(schema.Key{Key: []byte(`myTag1`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag1b.Key)
	assert.Equal(t, []byte(`item2`), tag1b.Value)
}

func TestStore_GetOnReferenceOnSameKeyMixReturnsAlwaysLastValue(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	idx1, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item1`)})
	idx2, _ := st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item2`)})
	_, _ = st.Set(schema.KeyValue{Key: []byte(`aaa`), Value: []byte(`item3`)})

	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag1`), Index: idx1})
	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag2`), Index: idx2})
	st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag3`), Key: []byte(`aaa`)})

	tag2, err := st.Get(schema.Key{Key: []byte(`myTag2`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag2.Key)
	assert.Equal(t, []byte(`item3`), tag2.Value)

	tag1, err := st.Get(schema.Key{Key: []byte(`myTag1`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag1.Key)
	assert.Equal(t, []byte(`item3`), tag1.Value)

	tag3, err := st.Get(schema.Key{Key: []byte(`myTag3`)})
	assert.NoError(t, err)
	assert.Equal(t, []byte(`aaa`), tag3.Key)
	assert.Equal(t, []byte(`item3`), tag3.Value)
}

func TestStore_ReferenceWrongKey(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	_, err := st.Reference(&schema.ReferenceOptions{Reference: []byte(`myTag1`), Key: []byte{tsPrefix}})
	assert.Equal(t, err, ErrInvalidKey)
}

func TestStore_ReferenceWrongReference(t *testing.T) {
	st, closer := makeStore()
	defer closer()

	_, err := st.Reference(&schema.ReferenceOptions{Key: []byte(`aaa`), Reference: []byte{tsPrefix}})
	assert.Equal(t, err, ErrInvalidReference)
}
