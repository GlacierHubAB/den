// Package desktop is Den's side of desktop control: an agent driving the
// human's Hyprland desktop through hypruse, inside a scope the human
// approved.
//
// hypruse is a third-party program (MIT, Copyright (c) 2026 Ilyas
// Khallouki). Den neither vendors nor modifies it: a desktop card's
// install fetches its package files from GitHub's archive of the pinned
// commit (`den install-hypruse` copies them from a clone the human names),
// into a venv of Den's own, each checked against the hashes below, and
// installs its dependencies from a hash-locked requirements file. Nothing
// of hypruse runs at install time; its LICENSE travels with it.
package desktop

// Pin is one pinned hypruse release: where it came from and the SHA-256 of
// every file Den copies from a clone of it.
type Pin struct {
	URL     string
	Commit  string
	Version string
	// Files maps a path inside the clone to its SHA-256 (hex). Those under
	// PackageDir make the hypruse package; LICENSE travels with them.
	Files map[string]string
}

// PackageDir is where hypruse's package lives in a clone.
const PackageDir = "src/hypruse"

// Pinned is hypruse 0.12.1 at commit 0035d86, the commit Den was reviewed
// against. The hashes were taken from a clean clone of that commit (git
// status empty, HEAD 0035d86db6679aca6a341331b8e60033a07d2670).
var Pinned = Pin{
	URL:     "https://github.com/IlyasKhallouki/hypruse",
	Commit:  "0035d86db6679aca6a341331b8e60033a07d2670",
	Version: "0.12.1",
	Files: map[string]string{
		"LICENSE":                   "d40a2cde9e2c34063411f13d58feb91fba71de2366460f485b07512ee179668c",
		"src/hypruse/__init__.py":   "40fdfac2a159f8b2383da9df6fe440fdbc0ba8732585d7ccdd2816b39435aa81",
		"src/hypruse/__main__.py":   "501536f8721e147643445b20d89518a443f8bf59db7f1c138c8b2f92f87ccdbd",
		"src/hypruse/a11y.py":       "deac52fbd4e4e2a0d8e13bfaefaf43fe49e0755fdb2063cd60d40d4e6afdc583",
		"src/hypruse/cli.py":        "bd5c493d8d0b8343f423d62279d4b26155c42ec55d5c5d074e6d2d11c4215ac0",
		"src/hypruse/cli_state.py":  "3b4df402063bed7ea5f8204b0b2144b9ad2148bb8f5e837a342ff7c27a709758",
		"src/hypruse/clipboard.py":  "bc797335d836d9f60c0a3f679249f95c75b0cf5580f2374ff266ccd8386c0dc8",
		"src/hypruse/events.py":     "396f677d4a92a4026287a983fb2f2b683181ec3ff20f524845176d318d33bfab",
		"src/hypruse/hyprctl.py":    "96b6e89a729c5e985e92608b0da523528b4cd4105b1e7c55b64f6254e1dd16b1",
		"src/hypruse/input.py":      "4db4e4e17fb2d247d4cb7f21bb5b68648fc6fc93c126ceec8a6a4a56da987569",
		"src/hypruse/journal.py":    "4a606753382789c27669d510ff500652b20b22a3f79587ef5362370e1ff782e5",
		"src/hypruse/safety.py":     "0ea3c888f27d1c85c7812c93b0a897c9982a5fd9cf52959ac46969b36402c64c",
		"src/hypruse/screenshot.py": "c981e7da4c91ab2ffb721b0b1b066d0dcc0aeae7ab78a76a52576a16167ddaa4",
		"src/hypruse/server.py":     "48c45fb8e2e2e8bde957b2bc1e3811d5594e38f673318d8e2ac3c4e3a1cc95c1",
		"src/hypruse/session.py":    "7fd54959d6cbb26c8e21559ea6ad443305314a0f9725b317d11b68ee9677ca88",
		"src/hypruse/skill.py":      "c013c5e4ba8ec0c2c6925162fd5ba00a697faef478bdcb7e447f7fe270c2cabd",
		"src/hypruse/trust.py":      "5fcad87db54fb49cbe5f889f28e97735f140f07eb4c01c393fcc56468aa5321a",
		"src/hypruse/verbs.py":      "f9efc060115874dbb532928b24009c14d1f0344e978ed2d23a393f52fefb98e8",
		"src/hypruse/wire.py":       "d04edcad8fb1b83f4870feccd61f96f0d3b6da23808346dc2b0c71b6dc6a380f",
	},
}
