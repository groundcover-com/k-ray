package cli

import (
	"strings"
	"testing"
)

const notSupported = "This command is not supported."

func render(pl *plan) []string {
	var out []string
	for _, c := range pl.calls {
		out = append(out, c.Tool+" "+string(c.json()))
	}
	return out
}

func TestTranslate(t *testing.T) {
	cases := []struct {
		name string
		argv string
		want []string // tool + JSON, keys sorted
	}{
		{"get list", "get pods", []string{`list {"kind":"pods"}`}},
		{"kubectl prefix", "kubectl get pods", []string{`list {"kind":"pods"}`}},
		{"all namespaces", "get pods -A", []string{`list {"allNamespaces":true,"kind":"pods"}`}},
		{"all namespaces long", "get pods --all-namespaces", []string{`list {"allNamespaces":true,"kind":"pods"}`}},
		{"flags before verb", "-n x get pods", []string{`list {"kind":"pods","namespace":"x"}`}},
		{"namespace forms", "get pods --namespace=x", []string{`list {"kind":"pods","namespace":"x"}`}},
		{"selector short attached", "get pods -lapp=api", []string{`list {"kind":"pods","labelSelector":"app=api"}`}},
		{"selector long", "get pods --selector app=api -n x", []string{`list {"kind":"pods","labelSelector":"app=api","namespace":"x"}`}},
		{"wide", "get pods -o wide", []string{`list {"kind":"pods"}`}},
		{"yaml no name", "get pods -o yaml", []string{`list {"kind":"pods"}`}},
		{"json no name", "get pods -ojson", []string{`list {"kind":"pods"}`}},
		{"get name", "get pod web-1 -n x", []string{`get {"kind":"pod","name":"web-1","namespace":"x"}`}},
		{"get name jsonpath", "get pod web-1 -o jsonpath={.spec.nodeName}", []string{`get {"jsonpath":"{.spec.nodeName}","kind":"pod","name":"web-1"}`}},
		{"get kind/name jsonpath spaced", "get deploy/web -o=jsonpath={.spec.replicas}", []string{`get {"jsonpath":"{.spec.replicas}","kind":"deploy","name":"web"}`}},
		{"get name yaml", "get pod web-1 -o yaml", []string{`get {"kind":"pod","name":"web-1"}`}},
		{"get name json", "get pod web-1 -o=json", []string{`get {"kind":"pod","name":"web-1"}`}},
		{"get slash", "get deploy/api -n x", []string{`get {"kind":"deploy","name":"api","namespace":"x"}`}},
		{"get existing form", "get deployment api -n x", []string{`get {"kind":"deployment","name":"api","namespace":"x"}`}},
		{"multi kind", "get deploy,svc", []string{`list {"kind":"deploy"}`, `list {"kind":"svc"}`}},
		{"all", "get all -n x", []string{
			`list {"kind":"pods","namespace":"x"}`, `list {"kind":"services","namespace":"x"}`,
			`list {"kind":"deployments","namespace":"x"}`, `list {"kind":"replicasets","namespace":"x"}`,
			`list {"kind":"statefulsets","namespace":"x"}`, `list {"kind":"daemonsets","namespace":"x"}`,
			`list {"kind":"jobs","namespace":"x"}`, `list {"kind":"cronjobs","namespace":"x"}`,
		}},
		{"ignored flags", "get pods --no-headers --show-labels --sort-by=.metadata.name --show-kind --ignore-not-found --request-timeout=5s", []string{`list {"kind":"pods"}`}},
		{"limit and full", "get pods --limit 5 --full", []string{`list {"full":true,"kind":"pods","limit":5}`}},
		{"match list", "get pods -A --match=img,Crash", []string{`list {"allNamespaces":true,"kind":"pods","match":"img,Crash"}`}},
		{"match events", "get events -n x --match probe", []string{`events {"match":"probe","namespace":"x"}`}},
		{"match events verb", "events --match probe-1", []string{`events {"match":"probe-1"}`}},
		{"match logs", "logs web-1 --match refused", []string{`logs {"match":"refused","pod":"web-1"}`}},
		{"get events", "get events -n x", []string{`events {"namespace":"x"}`}},
		{"get events all", "get events -A", []string{`events {"allNamespaces":true}`}},
		{
			"get events field selectors", "get events --field-selector involvedObject.name=web-1,involvedObject.kind=Pod,type=Warning",
			[]string{`events {"kind":"Pod","name":"web-1","warningsOnly":true}`},
		},
		{"get events field selector repeated eq", "get events --field-selector=involvedObject.name==web-1", []string{`events {"name":"web-1"}`}},
		{"events", "events", []string{`events {}`}},
		{"events for", "events --for pod/web-1 -n x", []string{`events {"kind":"pod","name":"web-1","namespace":"x"}`}},
		{"events types", "events --types=Warning -A", []string{`events {"allNamespaces":true,"warningsOnly":true}`}},
		{"events existing flags", "events --warnings --since 1h --limit 10", []string{`events {"limit":10,"since":"1h","warningsOnly":true}`}},
		{"describe", "describe pod web-1 -n x", []string{`describe {"kind":"pod","name":"web-1","namespace":"x"}`}},
		{"describe slash", "describe deploy/api", []string{`describe {"kind":"deploy","name":"api"}`}},
		{"describe kind only", "describe pods", []string{`list {"kind":"pods"}`}},
		{"describe kind selector", "describe pods -l app=api", []string{`describe {"kind":"pods","labelSelector":"app=api"}`}},
		{"describe selector all namespaces", "describe pod -l app=cartservice -A", []string{`describe {"allNamespaces":true,"kind":"pod","labelSelector":"app=cartservice"}`}},
		{"describe selector namespace", "describe deploy -l app=x -n shop", []string{`describe {"kind":"deploy","labelSelector":"app=x","namespace":"shop"}`}},
		{"describe name alone", "describe storefront", []string{`describe {"name":"storefront"}`}},
		{"describe name alone namespace", "describe storefront -n shop-web", []string{`describe {"name":"storefront","namespace":"shop-web"}`}},
		{"describe name alone all namespaces", "describe storefront -A", []string{`describe {"allNamespaces":true,"name":"storefront"}`}},
		{"describe kind name all namespaces", "describe pod web-1 -A", []string{`describe {"allNamespaces":true,"kind":"pod","name":"web-1"}`}},
		{"get events reason field selector", "get events --field-selector reason=Failed", []string{`events {"reason":"Failed"}`}},
		{"get events reason with others", "get events -A --field-selector type=Warning,reason=BackOff,involvedObject.name=web-1", []string{`events {"allNamespaces":true,"name":"web-1","reason":"BackOff","warningsOnly":true}`}},
		{"get events reason flag", "get events --reason Unhealthy -n x", []string{`events {"namespace":"x","reason":"Unhealthy"}`}},
		{"events reason flag", "events --reason=FailedScheduling", []string{`events {"reason":"FailedScheduling"}`}},
		{"logs pod", "logs web-1", []string{`logs {"pod":"web-1"}`}},
		{"logs tail equals", "logs web-1 --tail=20", []string{`logs {"pod":"web-1","tail":20}`}},
		{"logs tail space", "logs web-1 --tail 500 -n x", []string{`logs {"namespace":"x","pod":"web-1","tail":500}`}},
		{"logs tail all", "logs web-1 --tail=-1", []string{`logs {"pod":"web-1"}`}},
		{"logs container word", "logs web-1 app", []string{`logs {"container":"app","pod":"web-1"}`}},
		{"logs container flag", "logs web-1 -c app -p --since=10m", []string{`logs {"container":"app","pod":"web-1","previous":true,"since":"10m"}`}},
		{"logs previous long", "logs web-1 --previous", []string{`logs {"pod":"web-1","previous":true}`}},
		{"logs selector", "logs -l app=api --tail=50", []string{`logs {"labelSelector":"app=api","tail":50}`}},
		{"logs deployment", "logs deployment/checkout", []string{`logs {"workload":"deployment/checkout"}`}},
		{"logs deploy short", "logs deploy/checkout -n x --since=5m", []string{`logs {"namespace":"x","since":"5m","workload":"deployment/checkout"}`}},
		{"logs sts", "logs sts/db", []string{`logs {"workload":"statefulset/db"}`}},
		{"logs ds", "logs ds/agent", []string{`logs {"workload":"daemonset/agent"}`}},
		{"logs job", "logs job/migrate", []string{`logs {"workload":"job/migrate"}`}},
		{"logs rs", "logs rs/api-abc", []string{`logs {"workload":"replicaset/api-abc"}`}},
		{"logs kind space name", "logs deploy checkout", []string{`logs {"workload":"deployment/checkout"}`}},
		{"logs pod slash", "logs pod/web-1", []string{`logs {"pod":"web-1"}`}},
		{"logs ignored", "logs web-1 --timestamps --prefix --max-log-requests=10", []string{`logs {"pod":"web-1"}`}},
		{"top pods", "top pods", []string{`top {"resource":"pods"}`}},
		{"top pod", "top pod -n x", []string{`top {"namespace":"x","resource":"pods"}`}},
		{"top nodes", "top nodes", []string{`top {"resource":"nodes"}`}},
		{"top node", "top node", []string{`top {"resource":"nodes"}`}},
		{"top bare", "top -A --sort memory", []string{`top {"allNamespaces":true,"sortBy":"memory"}`}},
		{"top sort-by", "top pods --sort-by=memory -l app=api", []string{`top {"labelSelector":"app=api","resource":"pods","sortBy":"memory"}`}},
		{"rollout status slash", "rollout status deploy/api -n x", []string{`describe {"kind":"deploy","name":"api","namespace":"x"}`}},
		{"rollout status words", "rollout status deployment api", []string{`describe {"kind":"deployment","name":"api"}`}},
		{"context flags", "get pods --context c --kubeconfig /k", []string{`list {"kind":"pods"}`}},
		{"double dash", "get -n x -- pods", []string{`list {"kind":"pods","namespace":"x"}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pl, err := translate(strings.Fields(c.argv))
			if err != nil {
				t.Fatalf("%q: %v", c.argv, err)
			}
			got := render(pl)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("%q\n got: %q\nwant: %q", c.argv, got, c.want)
			}
		})
	}
}

func TestTranslateConnectionFlags(t *testing.T) {
	pl, err := translate(strings.Fields("get pods --context c1 --kubeconfig=/k --config /c --audit-log /a"))
	if err != nil {
		t.Fatal(err)
	}
	if pl.conn.context != "c1" || pl.conn.kubeconfig != "/k" || pl.conn.configPath != "/c" || pl.conn.auditLog != "/a" {
		t.Fatalf("%+v", pl.conn)
	}
}

func TestTranslateRefusals(t *testing.T) {
	// The fixed line, for everything that changes the cluster, opens a
	// session or is not a kubectl read command.
	for _, argv := range []string{
		"apply -f x.yaml", "create deployment x", "delete pod x", "edit deploy x", "patch pod x -p {}",
		"replace -f x", "scale deploy x --replicas=0", "autoscale deploy x", "set image deploy/x c=i",
		"label pod x a=b", "annotate pod x a=b", "taint nodes n k=v:NoSchedule", "cordon n", "uncordon n",
		"drain n", "exec x -- env", "exec -it x -- sh", "attach x", "port-forward x 80:80", "proxy", "cp x:/a /b",
		"run x --image=i", "expose deploy x", "debug x", "rollout restart deploy/x", "rollout undo deploy/x",
		"rollout pause deploy/x", "rollout resume deploy/x", "certificate approve x", "auth can-i get pods",
		"config view", "cluster-info", "explain pods", "api-resources", "wait --for=condition=Ready pod/x",
		"kubectl delete pod x", "kubectl frobnicate", "kubectl", "kubectl list pods", "kubectl version",
		"-n x delete pod y", "delete pod x --force --grace-period=0", "rollout",
	} {
		_, err := translate(strings.Fields(argv))
		if err == nil || err.Error() != notSupported {
			t.Errorf("%q: got %v", argv, err)
		}
	}
}

func TestTranslateUnsupportedReads(t *testing.T) {
	cases := []struct{ argv, want string }{
		{"logs web-1 -f", "--follow"},
		{"logs web-1 --follow", "--since or --tail"},
		{"get pods -w", "--watch"},
		{"get pods --watch", "--watch"},
		{"get pods -o jsonpath={.items[0].metadata.name}", "-o jsonpath is for one object"},
		{"get pod a -o jsonpath", "-o jsonpath needs a template"},
		{"get pod a -o jsonpath-file=x", "-o jsonpath-file is not supported"},
		{"get pods -o=custom-columns=A:.a", "-o custom-columns"},
		{"get pods -o go-template={{.}}", "-o go-template"},
		{"get pods -o name", "-o name"},
		{"get pods --field-selector status.phase=Running", "--field-selector"},
		{"logs web-1 --since-time=2020-01-01T00:00:00Z", "--since-time"},
		{"logs web-1 --all-containers", "--all-containers"},
		{"get pod web-1 --match x", "--match is for a list"},
		{"top pods --match x", "--match"},
		{"describe pod web-1 --match x", "--match"},
		{"rollout history deploy/x", "rollout history"},
		{"get pod a b", "Several names"},
		{"get pod/a pod/b", "Several names"},
		{"get pod a -l x=y", "-l"},
		{"get pods --bogus", "--bogus"},
		{"get", "kind"},
		{"get pods,svc a", "several kinds"},
		{"logs", "logs needs"},
		{"logs cronjob/x", "logs reads"},
		{"top pods web-1", "top by name"},
		{"top services", "top reads"},
		{"events pod/x", "--for"},
		{"events --types=Normal", "Normal"},
		{"get events --field-selector source=x", "source"},
		{"get pods --reason x", "--reason is for events"},
		{"describe -l x=y", "-l needs a kind"},
		{"logs web-1 --tail=x", "--tail"},
		{"get pods --tail=3", "--tail"},
	}
	for _, c := range cases {
		_, err := translate(strings.Fields(c.argv))
		if err == nil || err.Error() == notSupported || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want mention of %q", c.argv, err, c.want)
		}
	}
}

func TestMessagesNameNoProduct(t *testing.T) {
	for _, argv := range []string{
		"logs web-1 -f", "get pods -w", "get pods -o name", "get pod a b", "top pods x", "get pods --bogus",
		"rollout history deploy/x", "events --types=Normal", "logs cronjob/x",
	} {
		_, err := translate(strings.Fields(argv))
		if err == nil || strings.Contains(strings.ToLower(err.Error()), "kray") {
			t.Errorf("%q: %v", argv, err)
		}
	}
}

func TestTranslateNeverBuildsASecretCall(t *testing.T) {
	pl, err := translate(strings.Fields("get all -A"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range pl.calls {
		if strings.Contains(string(c.json()), "secret") {
			t.Fatalf("get all includes a secret call: %s", c.json())
		}
	}
}

func TestKubectlFormsAtCLILevel(t *testing.T) {
	kc := fakeCluster(t)
	code, out, errs := run("get", "pods", "-n", "shop", "--kubeconfig", kc)
	if code != 0 || !strings.Contains(out, "web-1") {
		t.Fatalf("get pods: %d %s %s", code, out, errs)
	}
	code, out, errs = run("kubectl", "get", "pods", "-o", "wide", "--kubeconfig", kc)
	if code != 0 || !strings.Contains(out, "web-1") {
		t.Fatalf("kubectl get pods -o wide: %d %s %s", code, out, errs)
	}
	code, out, errs = run("get", "pods,deploy", "-n", "shop", "--kubeconfig", kc)
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "error:") || code != 1 {
		t.Fatalf("get pods,deploy: %d %s %s", code, out, errs)
	}
	// The fake API server has only the pod list, so these reach the tools
	// and report the cluster's answer: proof that the tools ran.
	for _, argv := range [][]string{
		{"get", "pod", "web-1", "-o", "yaml"},
		{"describe", "deploy/api"},
		{"logs", "web-1", "--tail=20"},
		{"top", "pods"},
		{"events"},
	} {
		code, out, errs = run(append(argv, "-n", "shop", "--kubeconfig", kc)...)
		if strings.Contains(out+errs, notSupported) || strings.Contains(errs, "unknown") || strings.Contains(out, "name is required") {
			t.Fatalf("%v was not accepted: %d %s %s", argv, code, out, errs)
		}
		if !strings.Contains(out, "<<<cluster-data ") && code == 0 {
			t.Fatalf("%v: no tool output: %s", argv, out)
		}
	}
	code, out, _ = run("get", "secrets", "--kubeconfig", kc)
	if code != 1 || !strings.Contains(out, "never reads Secret objects") {
		t.Fatalf("get secrets: %d %s", code, out)
	}
	code, out, _ = run("get", "secret/db", "--kubeconfig", kc)
	if code != 1 || !strings.Contains(out, "never reads Secret objects") {
		t.Fatalf("get secret/db: %d %s", code, out)
	}
}

func TestRefusalLineAtCLILevel(t *testing.T) {
	// No kubeconfig is given: a refusal must not need a cluster.
	t.Setenv("KUBECONFIG", "/nonexistent")
	for _, argv := range [][]string{{"delete", "pod", "x"}, {"exec", "x", "--", "env"}, {"kubectl", "apply", "-f", "x"}} {
		code, out, errs := run(argv...)
		if code == 0 || out != "" || errs != notSupported+"\n" {
			t.Fatalf("%v: %d stdout=%q stderr=%q", argv, code, out, errs)
		}
	}
}

func TestOriginalCommandsStillRoute(t *testing.T) {
	for _, argv := range [][]string{{"version"}, {"list", "--help"}, {"serve", "--help"}, {"doctor", "--help"}} {
		if _, ok := (env{}).kubectl(argv); ok {
			t.Errorf("%v taken by the kubectl layer", argv)
		}
	}
	if code, _, errs := run("frobnicate"); code != 1 || strings.TrimSpace(errs) != "This command is not supported." {
		t.Fatalf("unknown word changed: %d %q", code, errs)
	}
}

// The usage says that a list can be filtered by name and that describe finds
// an object in any namespace, and tells the new forms.
func TestUsageNamesTheNewForms(t *testing.T) {
	_, _, errs := run()
	for _, want := range []string{
		"Every row of a list carries its cause on the same line, so the output can be filtered by name",
		"describe NAME finds an object in any namespace",
		"describe KIND -l SELECTOR",
		"--field-selector reason=Failed",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("usage lacks %q:\n%s", want, errs)
		}
	}
	if strings.Contains(strings.ToLower(errs), "claude") {
		t.Error("usage names an AI")
	}
}

// describe by name alone and by selector reach the tools through the CLI.
func TestDescribeFormsAtCLILevel(t *testing.T) {
	kc := fakeCluster(t)
	for _, argv := range [][]string{
		{"describe", "web-1"},
		{"describe", "pods", "-l", "app=web"},
	} {
		code, out, errs := run(append(argv, "-n", "shop", "--kubeconfig", kc)...)
		if strings.Contains(out+errs, notSupported) || strings.Contains(out, "name is required") || strings.Contains(out, "invalid arguments") {
			t.Fatalf("%v was not accepted: %d %s %s", argv, code, out, errs)
		}
	}
}

// A named object with no -n is found in another namespace: the kubectl forms
// reach the tools, and the tools look in all namespaces when the context
// namespace (shop) does not have it.
func TestNamedObjectWithoutNamespaceAtCLILevel(t *testing.T) {
	dep := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"ledger","namespace":"bank"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"ledger"}}},"status":{"replicas":1,"readyReplicas":1}}`
	pod := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"ledger-0","namespace":"bank"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}`
	kc := fakeClusterAnswering(t, map[string]string{
		"/apis/apps/v1/deployments":                        `{"kind":"DeploymentList","apiVersion":"apps/v1","metadata":{},"items":[` + dep + `]}`,
		"/apis/apps/v1/namespaces/bank/deployments/ledger": dep,
		"/api/v1/pods":                              `{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[` + pod + `]}`,
		"/api/v1/namespaces/bank/pods/ledger-0":     pod,
		"/api/v1/namespaces/bank/pods/ledger-0/log": "2026-11-13T11:00:00Z ledger started\n",
	})
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"get", "deploy", "ledger", "-o", "yaml"}, "found in namespace bank; none in the default namespace shop"},
		{[]string{"describe", "deploy", "ledger"}, "found in namespace bank; none in the default namespace shop"},
		{[]string{"logs", "ledger-0"}, "ledger started"},
	} {
		code, out, errs := run(append(c.argv, "--kubeconfig", kc)...)
		if code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("%v: %d\n%s\n%s", c.argv, code, out, errs)
		}
	}
	code, out, _ := run("get", "deploy", "ledger", "-n", "shop", "--kubeconfig", kc)
	if code != 1 || strings.Contains(out, "found in namespace") {
		t.Errorf("with -n there is no fallback: %d %s", code, out)
	}
}
