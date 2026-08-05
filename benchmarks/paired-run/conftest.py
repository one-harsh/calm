# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Make the paired-run modules importable regardless of pytest's invocation dir."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
