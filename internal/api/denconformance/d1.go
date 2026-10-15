// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT
// This file is part of the Den API conformance suite; the MIT licence text
// is in README.md beside it.

package denconformance

// D1Skip is Options.Skip for Den's first Drey interface: the subtests for
// starting and dropping missions and for view data, which that release
// does not serve yet. Every other subtest runs.
var D1Skip = []string{"mission.start", "mission.drop", "view.data"}
