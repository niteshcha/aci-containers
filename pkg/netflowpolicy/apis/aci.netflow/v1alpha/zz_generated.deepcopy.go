// +build !ignore_autogenerated

/***
Copyright 2019 Cisco Systems Inc. All rights reserved.

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

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1alpha

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetflowPolicy) DeepCopyInto(out *NetflowPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetflowPolicy.
func (in *NetflowPolicy) DeepCopy() *NetflowPolicy {
	if in == nil {
		return nil
	}
	out := new(NetflowPolicy)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *NetflowPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetflowPolicyList) DeepCopyInto(out *NetflowPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]NetflowPolicy, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetflowPolicyList.
func (in *NetflowPolicyList) DeepCopy() *NetflowPolicyList {
	if in == nil {
		return nil
	}
	out := new(NetflowPolicyList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *NetflowPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetflowPolicySpec) DeepCopyInto(out *NetflowPolicySpec) {
	*out = *in
	out.FlowSamplingPolicy = in.FlowSamplingPolicy
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetflowPolicySpec.
func (in *NetflowPolicySpec) DeepCopy() *NetflowPolicySpec {
	if in == nil {
		return nil
	}
	out := new(NetflowPolicySpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetflowPolicyStatus) DeepCopyInto(out *NetflowPolicyStatus) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetflowPolicyStatus.
func (in *NetflowPolicyStatus) DeepCopy() *NetflowPolicyStatus {
	if in == nil {
		return nil
	}
	out := new(NetflowPolicyStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetflowType) DeepCopyInto(out *NetflowType) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetflowType.
func (in *NetflowType) DeepCopy() *NetflowType {
	if in == nil {
		return nil
	}
	out := new(NetflowType)
	in.DeepCopyInto(out)
	return out
}
