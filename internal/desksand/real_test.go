package desksand

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/GlacierHubAB/den/internal/sysd"
)

// The test binary stands in for Den's: started as "desk-wrap" it runs the
// wrapper's stages, and as "desk-probe" the real test's probes, inside
// the sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "desk-wrap":
			Main(os.Args[2:])
		case "desk-probe":
			probeMain(os.Args[2:])
		}
	}
	os.Exit(m.Run())
}

// TestRealSandbox runs one launch exactly as Den does, through the user
// manager, pasta, the net stage and bwrap, with a probe in place of an
// app, and checks that every way out fails while the internet and DNS
// work. Only with DEN_REAL_SANDBOX=1, which the human approves each time.
func TestRealSandbox(t *testing.T) {
	if os.Getenv("DEN_REAL_SANDBOX") != "1" {
		t.Skip("set DEN_REAL_SANDBOX=1 to run a sandboxed probe on this machine's user manager")
	}
	bwrap, err := sysd.RootTool("bwrap")
	if err != nil {
		t.Fatal(err)
	}
	pasta, err := sysd.RootToolLink("pasta")
	if err != nil {
		t.Fatal(err)
	}
	run, way := os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("WAYLAND_DISPLAY")
	if run == "" || way == "" {
		t.Fatal("needs the session's XDG_RUNTIME_DIR and WAYLAND_DISPLAY")
	}
	// An isolated home on disk, with Den's folders in it and the probe
	// binary copied where the sandbox can see it (its /tmp is fresh).
	base := os.Getenv("DEN_REAL_SANDBOX_DIR")
	if !filepath.IsAbs(base) || strings.HasPrefix(base, "/tmp") {
		t.Fatal("set DEN_REAL_SANDBOX_DIR to an absolute folder on disk, not under /tmp (the sandbox's /tmp is fresh)")
	}
	home, err := os.MkdirTemp(base, "real-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	state := filepath.Join(home, ".local/state/den")
	cfg := filepath.Join(home, ".config/den")
	mission := filepath.Join(state, "desktop", "sandtest")
	for _, d := range []string{mission, cfg, filepath.Join(home, ".ssh"), Work(mission), Profile(mission, "probe"), filepath.Join(home, "bin")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(state, "pairing-keys"), []byte("plain-test-value"), 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "id_test"), []byte("plain-test-value"), 0o600)
	os.WriteFile(filepath.Join(mission, "mission.json"), []byte("{}"), 0o600)
	planted := filepath.Join(home, "planted.sock")
	if l, err := net.Listen("unix", planted); err == nil {
		defer l.Close()
	} else {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	probe := filepath.Join(home, "bin", "probe")
	if err := copyFile(self, probe); err != nil {
		t.Fatal(err)
	}

	// Listeners outside: the host's loopback, its LAN and global
	// addresses. Each must be out of reach.
	hosts := realHosts(t)
	var targets []string
	for _, a := range append([]netip.Addr{netip.MustParseAddr("127.0.0.1")}, hostTargets(hosts)...) {
		l, err := net.Listen("tcp", netip.AddrPortFrom(a, 0).String())
		if err != nil {
			t.Logf("no listener on %s: %v", a, err)
			continue
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		targets = append(targets, l.Addr().String())
	}

	resolv, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mission, "resolv.conf"), []byte("nameserver "+DNS.String()+"\n"), 0o644)
	s := Spec{
		Bwrap: bwrap, Pasta: pasta, Den: probe, UID: os.Getuid(), GID: os.Getgid(),
		Home: home, RuntimeDir: run, Wayland: way, DenState: state, DenConfig: cfg,
		Mission: mission, App: "probe", Resolv: resolv, Hosts: hosts, V6: hostV6(),
		Render: RenderNodes("/dev/dri"),
		Exists: func(p string) bool { _, err := os.Lstat(p); return err == nil },
		Env: map[string]string{"PATH": "/usr/bin:/bin", "HOME": home, "USER": os.Getenv("USER"), "LANG": "C.UTF-8",
			"XDG_RUNTIME_DIR": run, "WAYLAND_DISPLAY": way},
	}
	var nonce [6]byte
	rand.Read(nonce[:])
	escape := "den_sandtest_escape_" + hex.EncodeToString(nonce[:]) + ".service"
	out := filepath.Join(Work(mission), "probe.json")
	argv := append(Outer(s), "--", probe, "desk-probe", out, escape, planted, strings.Join(targets, ","))
	for _, a := range Allowed(s) {
		argv = append(argv, "--allow", a)
	}

	inst := hex.EncodeToString(nonce[:])
	slice := "den_desk_" + inst + "_sandtest.slice"
	svc := strings.TrimSuffix(slice, ".slice") + "_1.service"
	t.Cleanup(func() {
		sysd.Stop(slice)
		if st, err := sysd.State(escape); err == nil && st.Loaded {
			sysd.Stop(escape)
		}
	})
	if err := sysd.StartService(sysd.Service{Name: svc, Slice: slice, Description: "Den sandbox test", Argv: argv, Env: Env(s), Dir: Work(mission)}); err != nil {
		t.Fatal(err)
	}
	var res map[string]string
	deadline := time.Now().Add(90 * time.Second)
	for {
		if raw, err := os.ReadFile(out); err == nil && json.Unmarshal(raw, &res) == nil && res["done"] == "yes" {
			break
		}
		if time.Now().After(deadline) {
			st, _ := sysd.State(svc)
			t.Fatalf("no probe result in 90 s; the service is %+v (journalctl --user -u %s)", st, svc)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, k := range sortedKeys(res) {
		t.Logf("%-16s %s", k, res[k])
	}
	mustFail := []string{"systemd-run", "bus", "bus-activate", "systemd-private", "hypr", "cgroup-write", "cgroup-tmpfs", "cgroup-mount",
		"unshare-user", "route-delete", "write-home", "write-mission", "den-state", "ssh"}
	for _, k := range mustFail {
		if !strings.HasPrefix(res[k], "refused") {
			t.Errorf("%s was not refused: %s", k, res[k])
		}
	}
	for _, tg := range targets {
		if k := "net " + tg; !strings.HasPrefix(res[k], "refused") {
			t.Errorf("%s: %s", k, res[k])
		}
	}
	for _, k := range []string{"dns", "outside", "write-work", "cgroup-view", "env"} {
		if !strings.HasPrefix(res[k], "ok") {
			t.Errorf("%s: %s", k, res[k])
		}
	}
	if !strings.Contains(res["walk-home"], strconv.Quote(planted)) {
		t.Errorf("the walk missed the planted socket: %s", res["walk"])
	}

	// Cleanup: the slice stops and nothing of it is left, and the escape
	// unit never existed.
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
	if st, err := sysd.State(escape); err != nil || st.Loaded {
		t.Errorf("the escape unit %s: %+v %v", escape, st, err)
	}
}

// realHosts are the machine's addresses with their prefixes, as Den reads
// them at a launch.
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
	return HasV6Default(string(raw))
}

// hostTargets are the host's own LAN and global addresses to listen on:
// not loopback (tested on its own) and not link-local.
func hostTargets(hosts []netip.Prefix) []netip.Addr {
	var out []netip.Addr
	for _, h := range hosts {
		if a := h.Addr(); !a.IsLoopback() && !a.IsLinkLocalUnicast() {
			out = append(out, a)
		}
	}
	return out
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

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// probeMain runs inside the sandbox: each way out, tried; the results go
// to out, one word ("ok", "refused" or "reached") and what was seen.
func probeMain(args []string) {
	var allow []string
	if len(args) < 4 {
		os.Exit(2)
	}
	out, escape, planted, targets := args[0], args[1], args[2], args[3]
	for i := 4; i+1 < len(args); i += 2 {
		if args[i] == "--allow" {
			allow = append(allow, args[i+1])
		}
	}
	res := map[string]string{}
	refused := func(k string, err error, what string) {
		if err != nil {
			res[k] = "refused: " + err.Error()
		} else {
			res[k] = "reached: " + what
		}
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	uid := strconv.Itoa(os.Getuid())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	o, err := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--user", "--unit="+escape, "/bin/true").CombinedOutput()
	cancel()
	refused("systemd-run", err, string(o))

	var bus net.Conn
	res["bus"] = "refused: no session bus socket"
	for _, p := range []string{filepath.Join(run, "bus"), "/run/user/" + uid + "/bus"} {
		if c, err := net.DialTimeout("unix", p, 2*time.Second); err == nil {
			bus = c
			res["bus"] = "reached: " + p
		}
	}
	if bus != nil {
		// Reached: ask it to activate a made-up name (no such service).
		bus.Close()
		res["bus-activate"] = "reached: the session bus answered"
	} else {
		res["bus-activate"] = "refused: no session bus to ask"
	}
	_, err = net.DialTimeout("unix", filepath.Join(run, "systemd", "private"), 2*time.Second)
	refused("systemd-private", err, "the user manager's socket")
	_, err = os.Stat(filepath.Join(run, "hypr"))
	refused("hypr", err, "Hyprland's sockets")

	cg, _ := os.ReadFile("/proc/self/cgroup")
	if string(cg) == "0::/\n" {
		res["cgroup-view"] = "ok: 0::/"
	} else {
		res["cgroup-view"] = "reached: " + strings.TrimSpace(string(cg))
	}
	// Opened, never created: a file made on a tmpfs is no escape.
	if f, err := os.OpenFile("/sys/fs/cgroup/cgroup.procs", os.O_WRONLY, 0); err != nil {
		refused("cgroup-write", err, "")
	} else {
		_, err = f.WriteString(strconv.Itoa(os.Getpid()))
		f.Close()
		refused("cgroup-write", err, "wrote its pid to cgroup.procs")
	}
	if f, err := os.OpenFile("/sys/fs/cgroup/den-made", os.O_WRONLY|os.O_CREATE, 0o600); err == nil {
		f.Close()
		res["cgroup-tmpfs"] = "reached: made a file under /sys/fs/cgroup"
	} else {
		res["cgroup-tmpfs"] = "refused: " + err.Error()
	}
	mp := filepath.Join(os.TempDir(), "cg")
	os.Mkdir(mp, 0o700)
	err = syscall.Mount("none", mp, "cgroup2", 0, "")
	refused("cgroup-mount", err, "mounted cgroup2")

	c := exec.Command("/bin/true")
	c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	refused("unshare-user", c.Run(), "made a user namespace")
	refused("route-delete", deleteRoute(netip.MustParsePrefix("127.0.0.0/8")), "deleted a prohibit route")

	for _, tg := range strings.Split(targets, ",") {
		if tg == "" {
			continue
		}
		c, err := net.DialTimeout("tcp", tg, 3*time.Second)
		if c != nil {
			c.Close()
		}
		refused("net "+tg, err, "connected")
	}
	r := &net.Resolver{PreferGo: true}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	ips, err := r.LookupHost(ctx, "example.com")
	cancel()
	if err == nil {
		res["dns"] = "ok: " + strings.Join(ips, " ")
	} else {
		res["dns"] = "failed: " + err.Error()
	}
	if c, err := net.DialTimeout("tcp", "1.1.1.1:443", 10*time.Second); err == nil {
		c.Close()
		res["outside"] = "ok: 1.1.1.1:443"
	} else {
		res["outside"] = "failed: " + err.Error()
	}

	home := os.Getenv("HOME")
	refused("write-home", os.WriteFile(filepath.Join(home, "x"), []byte("x"), 0o600), "wrote in home")
	mission := filepath.Dir(filepath.Dir(out))
	refused("write-mission", os.WriteFile(filepath.Join(mission, "mission.json"), []byte("x"), 0o600), "rewrote mission.json")
	if err := os.WriteFile(filepath.Join(filepath.Dir(out), "x"), []byte("x"), 0o600); err == nil {
		res["write-work"] = "ok"
	} else {
		res["write-work"] = "failed: " + err.Error()
	}
	_, err = os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(mission)), "pairing-keys"))
	refused("den-state", err, "read Den's pairing keys")
	_, err = os.ReadFile(filepath.Join(home, ".ssh", "id_test"))
	refused("ssh", err, "read ~/.ssh")

	env := os.Environ()
	bad := []string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == "HYPRLAND_INSTANCE_SIGNATURE" || k == "DISPLAY" || k == "SSH_AUTH_SOCK" || strings.HasPrefix(k, "DEN_") ||
			k == "DBUS_SESSION_BUS_ADDRESS" && v != "disabled:" {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		res["env"] = "ok"
	} else {
		res["env"] = "reached: " + strings.Join(bad, " ")
	}
	// What pasta really set: the namespace's addresses and routes.
	if addrs, err := net.InterfaceAddrs(); err == nil {
		var as []string
		for _, a := range addrs {
			as = append(as, a.String())
		}
		res["ns-addrs"] = strings.Join(as, " ")
	}
	if raw, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6"); err == nil {
		res["ns disable_ipv6"] = strings.TrimSpace(string(raw))
	} else {
		res["ns disable_ipv6"] = "unreadable: " + err.Error()
	}
	for _, f := range []string{"/proc/net/route", "/proc/net/ipv6_route"} {
		raw, _ := os.ReadFile(f)
		res["ns "+filepath.Base(f)] = strings.Join(strings.Fields(string(raw)), " ")
	}
	began := time.Now()
	// The whole walk sees the human's home read-only: only counts, and
	// the paths of sockets (and abstract names), never other file names.
	var socks []string
	counts := map[string]int{}
	for _, f := range SelfWalk(allow) {
		kind, _, _ := strings.Cut(f, " ")
		counts[kind]++
		if kind == "socket" || kind == "abstract" {
			socks = append(socks, f)
		}
	}
	res["walk"] = fmt.Sprintf("sockets %d, unlisted folders %d (names left out), abstract %d, timed out %v; %s",
		counts["socket"], counts["unlisted"], counts["abstract"], counts["timeout"] > 0, strings.Join(socks, "; "))
	res["walk-time"] = time.Since(began).Round(time.Millisecond).String()
	// The planted socket, by a walk of the test's home alone: the whole
	// walk may stop at its bound before it gets there.
	res["walk-home"] = strings.Join(Walk([]string{home}, allow, time.Now().Add(WalkBound)), "; ")
	_ = planted
	res["done"] = "yes"
	raw, _ := json.MarshalIndent(res, "", " ")
	os.WriteFile(out, raw, 0o600)
	os.Exit(0)
}

// deleteRoute asks the kernel to delete a prohibit route, as an app
// trying to open the way back would.
func deleteRoute(p netip.Prefix) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	msg := routeMsg(p, 1)
	binary.NativeEndian.PutUint16(msg[4:], unix.RTM_DELROUTE)
	binary.NativeEndian.PutUint16(msg[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	if _, _, err := parseRoutes(buf[:n]); err != nil {
		return err
	}
	return nil // deleted: reached
}
