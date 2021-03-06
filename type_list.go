// Copyright 2017 Ross Peoples
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

package mydis

import (
	"bytes"
	"errors"

	"github.com/coreos/etcd/etcdserver"
	"github.com/gogo/protobuf/proto"
	"golang.org/x/net/context"
)

var (
	// ErrListEmpty signals that the List is empty.
	ErrListEmpty = errors.New("List is empty")
	// ErrListIndexOutOfRange signals that the given index is out of range of the list.
	ErrListIndexOutOfRange = errors.New("Index out of range")
)

// GetList from the cache.
func (s *Server) GetList(ctx context.Context, key *Key) (*List, error) {
	res, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	lst := &List{}
	if err := proto.Unmarshal(res.Value, lst); err != nil {
		return nil, err
	}
	return lst, nil
}

// GetListItem returns a single item from a list key.
func (s *Server) GetListItem(ctx context.Context, li *ListItem) (*ByteValue, error) {
	lst, err := s.GetList(ctx, &Key{Key: li.Key})
	if err != nil {
		return nil, err
	}
	length := int64(len(lst.Value))
	if length == 0 {
		return nil, ErrListEmpty
	}
	if li.Index-1 > length {
		li.Index = length - 1
	}
	if li.Index < 0 && li.Index*-1-1 > length {
		li.Index = 0
	}

	if li.Index >= 0 {
		return &ByteValue{Value: lst.Value[li.Index]}, nil
	}
	return &ByteValue{Value: lst.Value[length+li.Index]}, nil
}

// SetList sets a list to the cache.
func (s *Server) SetList(ctx context.Context, lst *List) (*Null, error) {
	key := lst.Key
	lst.Key = ""

	b, err := proto.Marshal(lst)
	if err != nil {
		return null, err
	}
	_, err = s.Set(ctx, &ByteValue{Key: key, Value: b})
	return null, err
}

// SetListItem sets a single item in a list, throws ErrListIndexOutOfRange if index is out of range.
func (s *Server) SetListItem(ctx context.Context, li *ListItem) (*Null, error) {
	key := &Key{Key: li.Key}
	if _, err := s.Lock(ctx, key); err != nil {
		return null, err
	}

	lst, err := s.GetList(ctx, key)
	if err != nil {
		s.Unlock(ctx, key)
		return null, err
	}
	length := int64(len(lst.Value))
	if length == 0 {
		s.Unlock(ctx, key)
		return null, ErrListEmpty
	}
	if li.Index-1 > length {
		s.Unlock(ctx, key)
		return null, ErrListIndexOutOfRange
	}
	if li.Index < 0 && li.Index*-1-1 > length {
		s.Unlock(ctx, key)
		return null, ErrListIndexOutOfRange
	}

	if li.Index >= 0 {
		lst.Value[li.Index] = li.Value
	} else {
		lst.Value[length+li.Index] = li.Value
	}

	lst.Key = li.Key
	return s.UnlockThenSetList(ctx, lst)
}

// ListLength returns the number of items in the list.
func (s *Server) ListLength(ctx context.Context, key *Key) (*IntValue, error) {
	lst, err := s.GetList(ctx, key)
	if err != nil {
		return &IntValue{}, err
	}
	return &IntValue{Value: int64(len(lst.Value))}, nil
}

// ListLimit sets the maximum length of a list, removing items from the top once limit is reached.
func (s *Server) ListLimit(ctx context.Context, li *ListItem) (*Null, error) {
	key := &Key{Key: li.Key}
	if _, err := s.Lock(ctx, key); err != nil {
		return null, err
	}

	lst, err := s.GetList(ctx, key)
	if err != nil {
		s.Unlock(ctx, key)
		return null, err
	}
	if li.Index < 0 {
		li.Index = 0
	}
	lst.Limit = li.Index
	lst.Key = li.Key
	return s.UnlockThenSetList(ctx, lst)
}

// ListInsert inserts a new item into the list at the given index, creates new list if doesn't exist.
func (s *Server) ListInsert(ctx context.Context, li *ListItem) (*Null, error) {
	key := &Key{Key: li.Key}
	if _, err := s.Lock(ctx, key); err != nil {
		return null, err
	}

	lst, err := s.GetList(ctx, key)
	if err == etcdserver.ErrKeyNotFound {
		lst = &List{Value: [][]byte{}}
	} else if err != nil {
		s.Unlock(ctx, key)
		return null, err
	}
	if li.Index < 0 {
		li.Index = 0
	}

	if li.Index >= int64(len(lst.Value)) {
		lst.Value = append(lst.Value, li.Value)
	} else {
		lst.Value = append(lst.Value, ZeroByte)
		copy(lst.Value[li.Index+1:], lst.Value[li.Index:])
		lst.Value[li.Index] = li.Value
	}

	lst.Key = li.Key
	enforceListLimit(lst)
	return s.UnlockThenSetList(ctx, lst)
}

// ListAppend appends an item to the end of a list, creates new list of doesn't exist.
func (s *Server) ListAppend(ctx context.Context, li *ListItem) (*Null, error) {
	key := &Key{Key: li.Key}
	if _, err := s.Lock(ctx, key); err != nil {
		return null, err
	}

	lst, err := s.GetList(ctx, key)
	if err == etcdserver.ErrKeyNotFound {
		lst = &List{Value: [][]byte{}}
	} else if err != nil {
		s.Unlock(ctx, key)
		return null, err
	}

	lst.Key = li.Key
	lst.Value = append(lst.Value, li.Value)
	enforceListLimit(lst)
	return s.UnlockThenSetList(ctx, lst)
}

// ListPopLeft removes and returns the first item in a list.
func (s *Server) ListPopLeft(ctx context.Context, key *Key) (*ByteValue, error) {
	if _, err := s.Lock(ctx, key); err != nil {
		return &ByteValue{}, err
	}

	lst, err := s.GetList(ctx, key)
	if err == etcdserver.ErrKeyNotFound {
		s.Unlock(ctx, key)
		return &ByteValue{}, ErrListEmpty
	} else if err != nil {
		s.Unlock(ctx, key)
		return &ByteValue{}, err
	}

	lst.Key = key.Key
	if len(lst.Value) == 0 {
		return &ByteValue{}, ErrListEmpty
	}

	b := lst.Value[0]
	lst.Value = lst.Value[1:]
	_, err = s.UnlockThenSetList(ctx, lst)
	if err != nil {
		return &ByteValue{}, err
	}
	return &ByteValue{Value: b}, nil
}

// ListPopRight removes and returns the last item in a list.
func (s *Server) ListPopRight(ctx context.Context, key *Key) (*ByteValue, error) {
	if _, err := s.Lock(ctx, key); err != nil {
		return &ByteValue{}, err
	}

	lst, err := s.GetList(ctx, key)
	if err == etcdserver.ErrKeyNotFound {
		s.Unlock(ctx, key)
		return &ByteValue{}, ErrListEmpty
	} else if err != nil {
		s.Unlock(ctx, key)
		return &ByteValue{}, err
	}

	lst.Key = key.Key
	if len(lst.Value) == 0 {
		return &ByteValue{}, ErrListEmpty
	}

	length := len(lst.Value)
	b := lst.Value[length-1]
	lst.Value = lst.Value[:length-1]
	_, err = s.UnlockThenSetList(ctx, lst)
	if err != nil {
		return &ByteValue{}, err
	}
	return &ByteValue{Value: b}, nil
}

// ListHas determines if the given value exists in the list, returns index or -1 if not found.
func (s *Server) ListHas(ctx context.Context, li *ListItem) (*IntValue, error) {
	lst, err := s.GetList(ctx, &Key{Key: li.Key})
	if err == etcdserver.ErrKeyNotFound {
		return &IntValue{Value: -1}, nil
	} else if err != nil {
		return &IntValue{}, err
	}

	for i, b := range lst.Value {
		if bytes.Equal(b, li.Value) {
			return &IntValue{Value: int64(i)}, nil
		}
	}
	return &IntValue{Value: -1}, nil
}

// ListDelete removes an item from a list by index.
func (s *Server) ListDelete(ctx context.Context, li *ListItem) (*Null, error) {
	key := &Key{Key: li.Key}
	if _, err := s.Lock(ctx, key); err != nil {
		return null, err
	}

	lst, err := s.GetList(ctx, key)
	if err != nil {
		s.Unlock(ctx, key)
		return null, err
	}
	length := int64(len(lst.Value))
	if length == 0 {
		s.Unlock(ctx, key)
		return null, ErrListEmpty
	}
	if li.Index < 0 {
		s.Unlock(ctx, key)
		return null, ErrListIndexOutOfRange
	}
	if li.Index-1 > length {
		s.Unlock(ctx, key)
		return null, ErrListIndexOutOfRange
	}

	copy(lst.Value[li.Index:], lst.Value[li.Index+1:])
	lst.Value[len(lst.Value)-1] = ZeroByte
	lst.Value = lst.Value[:len(lst.Value)-1]
	lst.Key = li.Key

	return s.UnlockThenSetList(ctx, lst)
}

// ListDeleteItem removes the first occurrence of value from a list, returns index of removed item or -1 for not found.
func (s *Server) ListDeleteItem(ctx context.Context, li *ListItem) (*IntValue, error) {
	key := &Key{Key: li.Key}
	if _, err := s.Lock(ctx, key); err != nil {
		return &IntValue{}, err
	}

	lst, err := s.GetList(ctx, key)
	if err == etcdserver.ErrKeyNotFound {
		s.Unlock(ctx, key)
		return &IntValue{Value: -1}, nil
	} else if err != nil {
		s.Unlock(ctx, key)
		return &IntValue{}, err
	}

	found := int64(-1)
	for i, b := range lst.Value {
		if bytes.Equal(b, li.Value) {
			found = int64(i)
			break
		}
	}

	if found == -1 {
		return &IntValue{Value: -1}, nil
	}

	copy(lst.Value[found:], lst.Value[found+1:])
	lst.Value[len(lst.Value)-1] = ZeroByte
	lst.Value = lst.Value[:len(lst.Value)-1]
	lst.Key = li.Key

	_, err = s.UnlockThenSetList(ctx, lst)
	return &IntValue{Value: found}, err
}
