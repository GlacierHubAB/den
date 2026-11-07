package wlsec

// The S4-6 real run: one launch of a held task's sandbox, through the
// user manager, pasta, Den's net stage and bwrap, with the listener bound
// at the Wayland path, and this test binary inside in place of an app.
// Inside, it lists the globals through the listener with this package's
// guarded client and tries a bind of each required global by its plain
// name. It sends no input and starts no compositor. Only with
// DEN_REAL_LISTENER=1, which the human approves each time.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/desksand"
	"github.com/GlacierHubAB/den/internal/sysd"
)

// required are the globals the listener must both hide and refuse a bind
// of: the probe's list, unchanged.
var required = []string{
	"zwp_virtual_keyboard_manager_v1",
	"zwlr_virtual_pointer_manager_v1",
	"zwp_input_method_manager_v2",
	"zwlr_foreign_toplevel_manager_v1",
	"zwlr_data_control_manager_v1",
	"ext_data_control_manager_v1",
	"hyprland_focus_grab_manager_v1",
	"hyprland_global_shortcuts_manager_v1",
	"wp_security_context_manager_v1",
}

// offeredPlain must be on the plain list, or the run proves nothing.
var offeredPlain = []string{"zwp_virtual_keyboard_manager_v1", "zwlr_virtual_pointer_manager_v1"}

// ordinary are what any app needs: the sandboxed list must have them.
var ordinary = []string{"wl_compositor", "wl_seat", "xdg_wm_base"}

// The test binary stands in for Den's: as "desk-wrap" it runs the
// sandbox's net stage, and as "desk-wlcheck" the checks inside.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "desk-wrap":
			desksand.Main(os.Args[2:])
		case "desk-wlcheck":
			checkMain(os.Args[2:])
		}
	}
	os.Exit(m.Run())
}

// plainList reads the compositor's globals on a plain connection, Den's
// side, with the guarded client: get_registry and a sync only.
func plainList(path string) (map[string]global, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	w := newConn(c, time.Now().Add(5*time.Second))
	reg := w.newID("wl_registry")
	if err := w.send(1, 1, nil, u32(reg)); err != nil {
		return nil, err
	}
	g := map[string]global{}
	return g, w.roundtrip(reg, g)
}

func TestRealListener(t *testing.T) {
	if os.Getenv("DEN_REAL_LISTENER") != "1" {
		t.Skip("set DEN_REAL_LISTENER=1 to run one sandboxed launch through a real listener in this session")
	}
	base := os.Getenv("DEN_REAL_LISTENER_DIR")
	if !filepath.IsAbs(base) || strings.HasPrefix(base, "/tmp") {
		t.Fatal("set DEN_REAL_LISTENER_DIR to an absolute folder on disk, not under /tmp (the sandbox's /tmp is fresh)")
	}
	run, way := os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("WAYLAND_DISPLAY")
	if run == "" || way == "" || filepath.IsAbs(way) || strings.Contains(way, "/") {
		t.Fatal("needs the session's XDG_RUNTIME_DIR and a WAYLAND_DISPLAY name")
	}
	raw := filepath.Join(run, way)
	bwrap, err := sysd.RootTool("bwrap")
	if err != nil {
		t.Fatal(err)
	}
	pasta, err := sysd.RootToolLink("pasta")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [6]byte
	rand.Read(nonce[:])
	inst := hex.EncodeToString(nonce[:])
	// What the run keeps: the results and the audit, in one new folder.
	keep := filepath.Join(base, "s4-6-"+inst)
	if err := os.Mkdir(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	wf, err := os.OpenFile(filepath.Join(keep, "wire-den.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer wf.Close()
	wire = wf
	t.Cleanup(func() { wire = nil })
	res := map[string]any{"instance": inst, "started": time.Now().Format(time.RFC3339)}
	defer func() {
		raw, _ := json.MarshalIndent(res, "", " ")
		os.WriteFile(filepath.Join(keep, "result.json"), append(raw, '\n'), 0o600)
	}()

	// 1. The plain list, from Den's side.
	plain, err := plainList(raw)
	if err != nil {
		t.Fatalf("plain list: %v", err)
	}
	res["plain"] = names(plain)
	for _, k := range offeredPlain {
		if _, ok := plain[k]; !ok {
			t.Fatalf("the plain list doesn't offer %s: the run would prove nothing", k)
		}
	}
	plainNames := map[string]uint32{}
	for _, k := range required {
		if g, ok := plain[k]; ok {
			plainNames[k] = g.name
		}
	}

	// 2. The listener, as Den makes it for a held task.
	parent := filepath.Join(run, "den-wl")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Chmod(parent, 0o700)
	l, err := Open(raw, filepath.Join(parent, "s4test"+inst), "io.glacierhub.den.desktop", "s4test"+inst, 5*time.Second)
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			l.Close()
		}
	}()

	// 3. One launch: an isolated home on disk, the checker copied where the
	// sandbox can see it.
	home := filepath.Join(keep, "home")
	mission := filepath.Join(home, ".local/state/den/desktop/s4test")
	cfg := filepath.Join(home, ".config/den")
	for _, d := range []string{mission, cfg, desksand.Work(mission), desksand.Profile(mission, "check"), filepath.Join(home, "bin")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	self, _ := os.Executable()
	check := filepath.Join(home, "bin", "check")
	if err := copyFile(self, check); err != nil {
		t.Fatal(err)
	}
	resolv, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mission, "resolv.conf"), []byte("nameserver "+desksand.DNS.String()+"\n"), 0o644)
	s := desksand.Spec{
		Bwrap: bwrap, Pasta: pasta, Den: check, UID: os.Getuid(), GID: os.Getgid(),
		Home: home, RuntimeDir: run, Wayland: way, WaylandListener: l.Path,
		DenState: filepath.Join(home, ".local/state/den"), DenConfig: cfg,
		Mission: mission, App: "check", Resolv: resolv, Hosts: realHosts(t), V6: hostV6(),
		Render: desksand.RenderNodes("/dev/dri"),
		Exists: func(p string) bool { _, err := os.Lstat(p); return err == nil },
		Env: map[string]string{"PATH": "/usr/bin:/bin", "HOME": home, "USER": os.Getenv("USER"), "LANG": "C.UTF-8",
			"XDG_RUNTIME_DIR": run, "WAYLAND_DISPLAY": way},
	}
	namesJSON, _ := json.Marshal(plainNames)
	out := filepath.Join(desksand.Work(mission), "check.json")
	wire2 := filepath.Join(desksand.Work(mission), "wire.log")
	argv := append(desksand.Outer(s), "--", check, "desk-wlcheck", out, wire2, string(namesJSON), l.Path)
	res["argv"] = argv
	slice := "den_desk_" + inst + "_s4test.slice"
	svc := strings.TrimSuffix(slice, ".slice") + "_1.service"
	t.Cleanup(func() { sysd.Stop(slice) })
	if err := sysd.StartService(sysd.Service{Name: svc, Slice: slice, Description: "Den S4-6 listener test", Argv: argv, Env: desksand.Env(s), Dir: desksand.Work(mission)}); err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	deadline := time.Now().Add(60 * time.Second)
	for {
		if b, err := os.ReadFile(out); err == nil && json.Unmarshal(b, &in) == nil && in["done"] == true {
			break
		}
		if time.Now().After(deadline) {
			st, _ := sysd.State(svc)
			t.Fatalf("no checker result in 60 s; the service is %+v (journalctl --user -u %s)", st, svc)
		}
		time.Sleep(200 * time.Millisecond)
	}
	res["inside"] = in
	if b, err := os.ReadFile(wire2); err == nil {
		os.WriteFile(filepath.Join(keep, "wire.log"), b, 0o600)
	}

	// 4. The end, in Den's order: the slice stopped, then the listener
	// closed; after that no new client connects.
	if err := sysd.Stop(slice); err != nil {
		t.Errorf("stop: %v", err)
	}
	for i := 0; ; i++ {
		st, err := sysd.State(slice)
		if err == nil && st.Gone() {
			break
		}
		if i > 75 {
			t.Fatalf("the slice is still there after 15 s: %+v %v", st, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	path := l.Path
	l.Close()
	closed = true
	time.Sleep(200 * time.Millisecond)
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		res["after_close"] = "connected"
		t.Error("a client connected to the listener after Den closed it")
	} else {
		res["after_close"] = "refused: " + err.Error() // Close also removes the path
	}

	// 5. The holds rule (the probe's, unchanged).
	holds := in["ordinary_ok"] == true && in["control_wl_compositor"] == "ok" && in["raw_unreachable"] == true
	req, _ := in["required"].(map[string]any)
	for _, k := range required {
		o, _ := req[k].(map[string]any)
		if o == nil {
			holds = false
			t.Errorf("%s: no result", k)
			continue
		}
		if o["listed_sandboxed"] == true {
			holds = false
			t.Errorf("%s is listed through the listener", k)
		}
		if _, offered := plainNames[k]; offered {
			if b, _ := o["bind_sandboxed"].(string); !strings.HasPrefix(b, "refused: ") {
				holds = false
				t.Errorf("%s: a bind through the listener gave %q", k, b)
			}
		}
	}
	if in["ordinary_ok"] != true || in["control_wl_compositor"] != "ok" {
		t.Errorf("ordinary %v, control %v", in["ordinary_ok"], in["control_wl_compositor"])
	}
	if in["raw_unreachable"] != true {
		t.Errorf("the compositor's own socket was reachable inside: %v", in["raw"])
	}
	res["holds"] = holds
	res["done"] = true
	t.Logf("holds=%v; results in %s", holds, keep)
}

// checkMain runs inside the sandbox: out and wireLog are where it writes,
// names the required globals' plain names, listener the outside path of
// the listener (which must not exist inside). It reads WAYLAND_DISPLAY and
// XDG_RUNTIME_DIR, as an app would.
func checkMain(args []string) {
	res := map[string]any{"done": false}
	if len(args) != 4 {
		os.Exit(2)
	}
	out, wireLog, listener := args[0], args[1], args[3]
	defer func() {
		if r := recover(); r != nil {
			res["error"] = fmt.Sprint(r)
		}
		b, _ := json.MarshalIndent(res, "", " ")
		os.WriteFile(out, b, 0o600)
		os.Exit(0)
	}()
	var plainNames map[string]uint32
	if err := json.Unmarshal([]byte(args[2]), &plainNames); err != nil {
		res["error"] = "names: " + err.Error()
		return
	}
	wf, err := os.OpenFile(wireLog, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		res["error"] = "wire log: " + err.Error()
		return
	}
	defer wf.Close()
	wire = wf
	run, way := os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("WAYLAND_DISPLAY")
	path := filepath.Join(run, way)
	until := time.Now().Add(15 * time.Second)

	// The sandboxed list, through the only Wayland path there is.
	sg, err := plainList(path)
	if err != nil {
		res["error"] = "sandboxed connection: " + err.Error()
		return
	}
	res["sandboxed"] = names(sg)
	ordinaryOK := true
	for _, k := range ordinary {
		if _, ok := sg[k]; !ok {
			ordinaryOK = false
		}
	}
	res["ordinary_ok"] = ordinaryOK
	control := "missing from the sandboxed list"
	if g, ok := sg["wl_compositor"]; ok {
		control = tryBind(path, g, until)
	}
	res["control_wl_compositor"] = control
	req := map[string]any{}
	for _, k := range required {
		_, listed := sg[k]
		o := map[string]any{"listed_sandboxed": listed}
		if n, ok := plainNames[k]; ok {
			o["offered_plain"] = true
			b := tryBind(path, global{name: n, iface: k, version: 1}, until)
			if b == "ok" {
				b = "REACHED"
			}
			o["bind_sandboxed"] = b
		}
		req[k] = o
	}
	res["required"] = req

	// The compositor's own socket can't be reached: the runtime folder holds
	// nothing Wayland's but the bound path, the listener's outside path
	// isn't there, and no other wayland-N name connects.
	rawRes := map[string]string{}
	unreachable := true
	ents, _ := os.ReadDir(run)
	var seen []string
	for _, e := range ents {
		seen = append(seen, e.Name())
	}
	sort.Strings(seen)
	rawRes["runtime_dir"] = strings.Join(seen, ",")
	for _, n := range seen {
		if strings.HasPrefix(n, "wayland") && n != way {
			unreachable = false
			rawRes["extra"] = n
		}
	}
	if _, err := os.Lstat(listener); err == nil {
		unreachable = false
		rawRes["listener_outside_path"] = "present"
	}
	for i := 0; i <= 9; i++ {
		n := "wayland-" + strconv.Itoa(i)
		if n == way {
			continue
		}
		if c, err := net.Dial("unix", filepath.Join(run, n)); err == nil {
			c.Close()
			unreachable = false
			rawRes[n] = "connected"
		}
	}
	if _, err := os.Lstat(filepath.Join(run, "hypr")); err == nil {
		unreachable = false
		rawRes["hypr"] = "present"
	}
	res["raw"] = rawRes
	res["raw_unreachable"] = unreachable
	res["done"] = true
}

// tryBind binds g on a fresh connection to path, then syncs: "ok" when
// the compositor took it, "refused: …" on a protocol error. The bound
// object gets no request: it has no row in allowed.
func tryBind(path string, g global, until time.Time) string {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return "no connection: " + err.Error()
	}
	defer c.Close()
	w := newConn(c, until)
	reg := w.newID("wl_registry")
	if err := w.send(1, 1, nil, u32(reg)); err != nil {
		return "send failed: " + err.Error()
	}
	if err := w.roundtrip(reg, map[string]global{}); err != nil {
		return "registry: " + err.Error()
	}
	id := w.newID(g.iface)
	if err := w.send(reg, 0, nil, u32(g.name), str(g.iface), u32(1), u32(id)); err != nil {
		return "send failed: " + err.Error()
	}
	err = w.roundtrip(0, nil)
	var r errRefused
	switch {
	case errors.As(err, &r):
		return "refused: " + r.msg
	case err != nil:
		return "connection ended: " + err.Error()
	}
	return "ok"
}

func names(m map[string]global) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func realHosts(t *testing.T) []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Prefix
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				bits, _ := n.Mask.Size()
				ip = ip.Unmap()
				if ip.Is4() && bits > 32 {
					bits -= 96
				}
				out = append(out, netip.PrefixFrom(ip, bits))
			}
		}
	}
	return out
}

func hostV6() bool {
	raw, _ := os.ReadFile("/proc/net/ipv6_route")
	return desksand.HasV6Default(string(raw))
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}
