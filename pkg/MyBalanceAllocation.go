package MyBalanceAllocation

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"

	"github.com/thedevsaddam/gojsonq/v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	runtime2 "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	metricsClientSet "k8s.io/metrics/pkg/client/clientset/versioned"
)

const Name = "MyBalanceAllocation"
const NodeDataJsonPath = "data.result.[0].value.[1]"

type MyBalanceAllocationPlugin struct {
	handle framework.Handle
	args   MyBalanceAllocationPluginArg
}

type MyBalanceAllocationPluginArg struct {
	PrometheusEndpoint string `json:"prometheus_endpoint,omitempty"`
	MaxMemory          int    `json:"max_memory,omitempty"`
	MetricsClientSet   *metricsClientSet.Clientset
}

func (n *MyBalanceAllocationPlugin) Name() string {
	return Name
}

func (n *MyBalanceAllocationPlugin) Score(_ context.Context, _ *framework.CycleState, _ *v1.Pod, nodeName string) (int64, *framework.Status) {
	nodeInfo, err := n.handle.SnapshotSharedLister().NodeInfos().Get(nodeName) //获取快照中所有可用的Node，即经过预选阶段来到打分阶段的节点
	if err != nil {
		return 0, framework.AsStatus(err)
	}
	node_cpu_fraction, err := n.getNodeCpuFraction(nodeName)
	node_mem_fraction, err := n.getNodeMemFraction(nodeName)
	node_net_fraction, err := n.getNodeNetFraction(nodeName)
	node_cpu_core_num, err := n.getNodeCpuCoreNum(nodeName)
	node_mem_allocatable, err := n.getNodeMemAllocatable(nodeName)
	Node_P := 0.5*float64(node_cpu_core_num) + 0.3*float64(node_mem_allocatable)/1000000000 + 0.2
	Node_L := 0.5*node_cpu_fraction + 0.3*node_mem_fraction + 0.2*node_net_fraction
	score := int64((1 - Node_L/Node_P) * 100) //打分
	klog.Infof("nodeInfo: %s node %s counting detail:, score %v", nodeInfo, nodeName, score)
	return score, nil
}

func New(configuration runtime.Object, f framework.Handle) (framework.Plugin, error) {
	args := &MyBalanceAllocationPluginArg{}
	err := runtime2.DecodeInto(configuration, args)

	config, err := GetClientConfig()
	if err != nil {
		return nil, err
	}
	mcs, err := metricsClientSet.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	args.MetricsClientSet = mcs

	return &MyBalanceAllocationPlugin{
		handle: f,
		args:   *args,
	}, nil
}

func (n *MyBalanceAllocationPlugin) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

// prometheus获取节点CPU利用率
func (n *MyBalanceAllocationPlugin) getNodeCpuFraction(nodeName string) (float64, error) {
	queryString := fmt.Sprintf("instance:node_cpu_utilisation:rate1m{instance=\"%s\"}", nodeName)
	r, err := http.Get(fmt.Sprintf("http://192.168.146.100:30090/api/v1/query?query=%s", queryString))
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			panic(err)
		}
	}(r.Body)
	jsonString, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return 0, err
	}
	cpuFraction, err := ParseDataToFloat(string(jsonString))
	if err != nil {
		return 0, err
	}
	return cpuFraction, nil
}

// prometheus获取节点内存利用率
func (n *MyBalanceAllocationPlugin) getNodeMemFraction(nodeName string) (float64, error) {
	queryString := fmt.Sprintf("instance:node_memory_utilisation:ratio{instance=\"%s\"}", nodeName)
	r, err := http.Get(fmt.Sprintf("http://192.168.146.100:30090/api/v1/query?query=%s", queryString))
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			panic(err)
		}
	}(r.Body)
	jsonString, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return 0, err
	}
	memFraction, err := ParseDataToFloat(string(jsonString))
	if err != nil {
		return 0, err
	}
	return memFraction, nil
}

// prometheus获取节点网络带宽指标
func (n *MyBalanceAllocationPlugin) getNodeNetFraction(nodeName string) (float64, error) {
	queryString := fmt.Sprintf("instance:node_network_receive_bytes_excluding_lo:rate1m{instance=\"%s\"}", nodeName)
	r, err := http.Get(fmt.Sprintf("http://192.168.146.100:30090/api/v1/query?query=%s", queryString))
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			panic(err)
		}
	}(r.Body)
	jsonString, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return 0, err
	}
	netDownloadSpeed, err := ParseDataToFloat(string(jsonString))
	if err != nil {
		return 0, err
	}
	return netDownloadSpeed / 10000000, nil
}

// prometheus获取节点内存总容量（认为是可分配内存）
func (n *MyBalanceAllocationPlugin) getNodeMemAllocatable(nodeName string) (int64, error) {
	queryString := fmt.Sprintf("kube_node_status_allocatable_memory_bytes{node=\"%s\"}", nodeName)
	r, err := http.Get(fmt.Sprintf("http://192.168.146.100:30090/api/v1/query?query=%s", queryString))
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			panic(err)
		}
	}(r.Body)
	jsonString, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return 0, err
	}
	memAllocatable, err := ParseDataToInt(string(jsonString))
	if err != nil {
		return 0, err
	}
	return memAllocatable, nil
}

// prometheus获取节点网络带宽（认为是主机最大下载速度）
func (n *MyBalanceAllocationPlugin) getNodeCpuCoreNum(nodeName string) (int64, error) {
	queryString := fmt.Sprintf("kube_node_status_allocatable_cpu_cores{node=\"%s\"}", nodeName)
	r, err := http.Get(fmt.Sprintf("http://%s/api/v1/query?query=%s", queryString))
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
			panic(err)
		}
	}(r.Body)
	jsonString, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return 0, err
	}
	cpuCoreNum, err := ParseDataToInt(string(jsonString))
	if err != nil {
		return 0, err
	}
	return cpuCoreNum, nil
}

//解析HTTP API返回来的数据
func ParseDataToInt(responseString string) (int64, error) { //输入是json数据
	r := gojsonq.New().FromString(responseString).Find(NodeDataJsonPath)
	//获取json中第一个的结果，第二个值，这个要根据自己去curl 一下获得的json数据来看
	return strconv.ParseInt(r.(string), 10, 64) //两个数字代表10进制，int64的数据
}
func ParseDataToFloat(responseString string) (float64, error) { //输入是json数据
	r := gojsonq.New().FromString(responseString).Find(NodeDataJsonPath)
	//获取json中第一个的结果，第二个值，这个要根据自己去curl 一下获得的json数据来看
	return strconv.ParseFloat(r.(string), 64)
}
func GetClientConfig() (*rest.Config, error) { //获取用户配置
	config, err := rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", os.Getenv("HOME")+"/.kube/config")
		if err != nil {
			return nil, err
		}
	}
	return config, nil
}
