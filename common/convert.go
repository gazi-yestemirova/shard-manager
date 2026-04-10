// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package common

import (
	"time"
)

// Returns a pointer to the given value.
// todo (david.porter) Remove the remaining helpers here which are now obsolete
func Ptr[T any](v T) *T {
	return &v
}

// IntPtr makes a copy and returns the pointer to an int.
func IntPtr(v int) *int {
	return &v
}

// Int16Ptr makes a copy and returns the pointer to an int16.
func Int16Ptr(v int16) *int16 {
	return &v
}

// Int32Ptr makes a copy and returns the pointer to an int32.
func Int32Ptr(v int32) *int32 {
	return &v
}

// Int64Ptr makes a copy and returns the pointer to an int64.
func Int64Ptr(v int64) *int64 {
	return &v
}

// Uint32Ptr makes a copy and returns the pointer to a uint32.
func Uint32Ptr(v uint32) *uint32 {
	return &v
}

// Uint64Ptr makes a copy and returns the pointer to a uint64.
func Uint64Ptr(v uint64) *uint64 {
	return &v
}

// Float64Ptr makes a copy and returns the pointer to an int64.
func Float64Ptr(v float64) *float64 {
	return &v
}

// BoolPtr makes a copy and returns the pointer to a bool.
func BoolPtr(v bool) *bool {
	return &v
}

// StringPtr makes a copy and returns the pointer to a string.
func StringPtr(v string) *string {
	return &v
}

// TimeNowNanosPtr returns an int64 ptr to current time in unix nanos
func TimeNowNanosPtr() *int64 {
	v := time.Now().UnixNano()
	return &v
}

// TimePtr makes a copy and returns the pointer to a time
func TimePtr(v time.Time) *time.Time {
	return &v
}

// DurationPtr makes a copy and returns the pointer to a duration
func DurationPtr(v time.Duration) *time.Duration {
	return &v
}

// StringDefault returns value if string pointer is set otherwise default value of string
func StringDefault(v *string) string {
	var defaultString string
	if v == nil {
		return defaultString
	}
	return *v
}

// Int32Default returns value if int32 pointer is set otherwise default value of int32
func Int32Default(v *int32) int32 {
	var defaultInt32 int32
	if v == nil {
		return defaultInt32
	}
	return *v
}

// Int64Default returns value if int64 pointer is set otherwise default value of int64
func Int64Default(v *int64) int64 {
	var defaultInt64 int64
	if v == nil {
		return defaultInt64
	}
	return *v
}

// BoolDefault returns value if bool pointer is set otherwise default value of bool
func BoolDefault(v *bool) bool {
	var defaultBool bool
	if v == nil {
		return defaultBool
	}
	return *v
}
