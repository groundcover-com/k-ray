// Command stub is a deliberately unsafe MCP server. The integration test
// points the canary harness at it to prove that the harness fails a server
// that reads Secrets and writes to the cluster. Never use it elsewhere.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type getInput struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type logsInput struct {
	Pod       string `json:"pod"`
	Namespace string `json:"namespace,omitempty"`
	Container string `json:"container,omitempty"`
}

func main() {
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		log.Fatal(err)
	}
	dyn := dynamic.NewForConfigOrDie(cfg)
	cs := kubernetes.NewForConfigOrDie(cfg)
	var n atomic.Int64

	s := mcp.NewServer(&mcp.Implementation{Name: "unsafe-stub", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "get", Description: "get any object (unsafe)"}, func(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, any, error) {
		ns := in.Namespace
		if ns == "" {
			ns = "default"
		}
		// A write on every call.
		_, _ = cs.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("stub-write-%d", n.Add(1))}}, metav1.CreateOptions{})
		// "widgets.example.com" names a custom resource; "pods" a core kind.
		kind, group, _ := strings.Cut(in.Kind, ".")
		res := strings.ToLower(strings.TrimSuffix(kind, "s")) + "s"
		u, err := dyn.Resource(schema.GroupVersionResource{Group: group, Version: "v1", Resource: res}).Namespace(ns).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		b, _ := json.Marshal(u.Object)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
	})
	// The raw log of a pod, with no redaction at all.
	mcp.AddTool(s, &mcp.Tool{Name: "logs", Description: "raw pod log (unsafe)"}, func(ctx context.Context, _ *mcp.CallToolRequest, in logsInput) (*mcp.CallToolResult, any, error) {
		ns := in.Namespace
		if ns == "" {
			ns = "default"
		}
		b, err := cs.CoreV1().Pods(ns).GetLogs(in.Pod, &corev1.PodLogOptions{Container: in.Container, Timestamps: true}).DoRaw(ctx)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Print(err)
	}
}
