// Copyright 2020 Cisco Systems, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"github.com/sirupsen/logrus"

	netflowpolicy "github.com/noironetworks/aci-containers/pkg/netflowpolicy/apis/aci.netflow/v1alpha"
	netflowclientset "github.com/noironetworks/aci-containers/pkg/netflowpolicy/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"

	"github.com/noironetworks/aci-containers/pkg/apicapi"
)

const (
	netflowCRDName = "netflowpolicies.aci.netflow"
)

func NetflowPolicyLogger(log *logrus.Logger, netflow *netflowpolicy.NetflowPolicy) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"name": netflow.ObjectMeta.Name,
		"spec": netflow.Spec,
	})
}

func netflowInit(cont *AciController, stopCh <-chan struct{}) {
	cont.log.Debug("Initializing netflow client")
	restconfig := cont.env.RESTConfig()
	netflowClient, err := netflowclientset.NewForConfig(restconfig)
	if err != nil {
		cont.log.Errorf("Failed to intialize netflow client")
		return
	}
	cont.initNetflowInformerFromClient(netflowClient)
	cont.netflowInformer.Run(stopCh)
}

func (cont *AciController) initNetflowInformerFromClient(
	netflowClient *netflowclientset.Clientset) {
	cont.initNetflowInformerBase(
		cache.NewListWatchFromClient(
			netflowClient.AciV1alpha().RESTClient(), "netflowpolicies",
			metav1.NamespaceAll, fields.Everything()))
}

func (cont *AciController) initNetflowInformerBase(listWatch *cache.ListWatch) {
	cont.netflowIndexer, cont.netflowInformer = cache.NewIndexerInformer(
		listWatch,
		&netflowpolicy.NetflowPolicy{}, 0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				cont.netflowPolicyUpdated(obj)
			},
			UpdateFunc: func(_, obj interface{}) {
				cont.netflowPolicyUpdated(obj)
			},
			DeleteFunc: func(obj interface{}) {
				cont.netflowPolicyDelete(obj)
			},
		},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	cont.log.Debug("Initializing Netflow Policy Informers")

}

func (cont *AciController) netflowPolicyUpdated(obj interface{}) {
	netflowPolicy := obj.(*netflowpolicy.NetflowPolicy)
	key, err := cache.MetaNamespaceKeyFunc(netflowPolicy)
	if err != nil {
		NetflowPolicyLogger(cont.log, netflowPolicy).
			Error("Could not create key:" + err.Error())
		return
	}
	cont.queueNetflowUpdateByKey(key)

}

func (cont *AciController) queueNetflowUpdateByKey(key string) {
	cont.netflowQueue.Add(key)
}

func (cont *AciController) queueNetflowUpdate(netflowpolicy *netflowpolicy.NetflowPolicy) {
	key, err := cache.MetaNamespaceKeyFunc(netflowpolicy)
	if err != nil {
		NetflowPolicyLogger(cont.log, netflowpolicy).
			Error("Could not create key:" + err.Error())
		return
	}
	cont.netflowQueue.Add(key)
}

func (cont *AciController) netflowPolicyDelete(obj interface{}) {
	nf, isNf := obj.(*netflowpolicy.NetflowPolicy)
	if !isNf {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			NetflowPolicyLogger(cont.log, nf).
				Error("Received unexpected object: ", obj)
			return
		}
		nf, ok = deletedState.Obj.(*netflowpolicy.NetflowPolicy)
		if !ok {
			NetflowPolicyLogger(cont.log, nf).
				Error("DeletedFinalStateUnknown contained non-netflow object: ", deletedState.Obj)
			return
		}
	}
	nfkey, err := cache.MetaNamespaceKeyFunc(nf)
	if err != nil {
		NetflowPolicyLogger(cont.log, nf).
			Error("Could not create netflow key: ", err)
		return
	}
	cont.apicConn.ClearApicObjects(cont.aciNameForKey("nfp", nfkey))

}

func (cont *AciController) handleNetflowPolUpdate(obj interface{}) bool {
	nfp, ok := obj.(*netflowpolicy.NetflowPolicy)
	if !ok {
		cont.log.Error("handleNetflowPolUpdate: Bad object type")
		return false
	}
	logger := NetflowPolicyLogger(cont.log, nfp)
	key, err := cache.MetaNamespaceKeyFunc(nfp)
	if err != nil {
		logger.Error("Could not create netflow policy key: ", err)
		return false
	}
	labelKey := cont.aciNameForKey("nfp", key)
	cont.log.Debug("create netflowpolicy")
	nf := apicapi.NewNetflowVmmExporterPol(labelKey)
	nfDn := nf.GetDn()
	apicSlice := apicapi.ApicSlice{nf}
	nf.SetAttr("dstAddr", nfp.Spec.FlowSamplingPolicy.DstAddr)
	nf.SetAttr("dstPort", nfp.Spec.FlowSamplingPolicy.DstPort)
	if nfp.Spec.FlowSamplingPolicy.Version == "netflow" {
		nf.SetAttr("ver", "v5")
	}
	if nfp.Spec.FlowSamplingPolicy.Version == "ipfix" {
		nf.SetAttr("ver", "v9")
	}

	VmmVSwitch := apicapi.NewVmmVSwitchPolicyCont(cont.vmmDomainProvider(), cont.config.AciVmmDomain)
	RsVmmVSwitch := apicapi.NewVmmRsVswitchExporterPol(cont.vmmDomainProvider(), cont.config.AciVmmDomain, nfDn)
	VmmVSwitch.AddChild(RsVmmVSwitch)
	RsVmmVSwitch.SetAttr("activeFlowTimeOut", nfp.Spec.FlowSamplingPolicy.ActiveFlowTimeOut)
	RsVmmVSwitch.SetAttr("idleFlowTimeOut", nfp.Spec.FlowSamplingPolicy.IdleFlowTimeOut)
	apicSlice = append(apicSlice, VmmVSwitch)

	cont.log.Info("create netflow Rs", apicSlice)

	cont.apicConn.WriteApicObjects(labelKey, apicSlice)

	return false

}
