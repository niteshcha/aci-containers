// Copyright 2019 Cisco Systems, Inc.
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
	"github.com/Sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	snatclientset "github.com/noironetworks/aci-containers/pkg/snatpolicy/clientset/versioned"
	snatpolicy "github.com/noironetworks/aci-containers/pkg/snatpolicy/apis/aci.snat/v1"
)

type ContLabel struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

type ContPodSelector struct {
	Labels     []ContLabel
	Deployment string
	Namespace  string
}

type ContPortRange struct {
	Start int `json:"start,omitempty"`
	End   int `json:"end,omitempty"`
}

type ContSnatPolicy struct {
	SnatIp    []string
	Selector  ContPodSelector
	PortRange []ContPortRange
	Protocols []string
}

func SnatPolicyLogger(log *logrus.Logger, snat *snatpolicy.SnatPolicy) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"namespace": snat.ObjectMeta.Namespace,
		"name":      snat.ObjectMeta.Name,
		"spec":      snat.Spec,
	})
}

func (cont *AciController) initSnatInformerFromClient(
	snatClient *snatclientset.Clientset) {
	cont.initSnatInformerBase(
		cache.NewListWatchFromClient(
			snatClient.AciV1().RESTClient(), "snatpolicies",
			metav1.NamespaceAll, fields.Everything()))
}

func (cont *AciController) initSnatInformerBase(listWatch *cache.ListWatch) {
	cont.snatIndexer, cont.snatInformer = cache.NewIndexerInformer(
		listWatch,
		&snatpolicy.SnatPolicy{}, 0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				cont.snatUpdated(obj)
			},
			UpdateFunc: func(_ interface{}, obj interface{}) {
				cont.snatUpdated(obj)
			},
			DeleteFunc: func(obj interface{}) {
				cont.snatPolicyDelete(obj)
			},
		},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	cont.log.Debug("Initializing Snat Policy Informers")

}

func(cont *AciController) snatUpdated(obj interface{}) {
	snat := obj.(*snatpolicy.SnatPolicy)
	key, err := cache.MetaNamespaceKeyFunc(snat)
	if err != nil {
		SnatPolicyLogger(cont.log, snat).
			Error("Could not create key:" + err.Error())
		return
	}
	cont.queueSnatUpdateByKey(key)
}

func (cont *AciController) queueSnatUpdateByKey(key string) {
	cont.snatQueue.Add(key)
}

func (cont *AciController) queueSnatUpdate(snatpolicy *snatpolicy.SnatPolicy) {
	key, err := cache.MetaNamespaceKeyFunc(snatpolicy)
	if err != nil {
		SnatPolicyLogger(cont.log, snatpolicy).
			Error("Could not create key:" + err.Error())
		return
	}
	cont.snatQueue.Add(key)
}

func (cont *AciController) handleSnatUpdate(snatpolicy *snatpolicy.SnatPolicy) bool {
	_, err := cache.MetaNamespaceKeyFunc(snatpolicy)
	if err != nil {
		SnatPolicyLogger(cont.log, snatpolicy).
			Error("Could not create key:" + err.Error())
		return false
	}

	policyName := snatpolicy.ObjectMeta.Name
	var requeue bool
	cont.indexMutex.Lock()
	cont.updateSnatPolicyCache(policyName, snatpolicy)
	cont.indexMutex.Unlock()

	cont.indexMutex.Lock()
	if cont.snatSyncEnabled {
		cont.indexMutex.Unlock()
		err = cont.updateServiceDeviceInstanceSnat("MYSNAT")
		if err == nil {
			requeue = true
		}
	} else {
		cont.indexMutex.Unlock()
	}
	return requeue
}

func (cont *AciController) updateSnatPolicyCache(key string, snatpolicy *snatpolicy.SnatPolicy) {
	var policy ContSnatPolicy
	policy.SnatIp = snatpolicy.Spec.SnatIp
	snatLabels := snatpolicy.Spec.Selector.Labels
	snatDeploy := snatpolicy.Spec.Selector.Deployment
	snatNS := snatpolicy.Spec.Selector.Namespace
	var labels []ContLabel
	for _, val := range snatLabels {
		lab := ContLabel{Key: val.Key, Value: val.Value}
		labels = append(labels, lab)
	}
	policy.Selector = ContPodSelector{Labels: labels, Deployment: snatDeploy, Namespace: snatNS}
	cont.snatPolicyCache[key] = &policy
}

func (cont *AciController) snatPolicyDelete(snatobj interface{}) {
        snatpolicy := snatobj.(*snatpolicy.SnatPolicy)
	cont.indexMutex.Lock()
	delete(cont.snatPolicyCache, snatpolicy.ObjectMeta.Name)

        if len(cont.snatPolicyCache) == 0 {
                cont.log.Debug("No more snat policies, deleting graph")
		graphName := cont.aciNameForKey("snat", "MYSNAT")
		go cont.apicConn.ClearApicObjects(graphName)
        } else {
		go cont.updateServiceDeviceInstanceSnat("MYSNAT")
	}
	cont.indexMutex.Unlock()
}

func (cont *AciController) snatFullSync() {
	cache.ListAll(cont.snatIndexer, labels.Everything(),
		func(sobj interface{}) {
			cont.queueSnatUpdate(sobj.(*snatpolicy.SnatPolicy))
		})
}
