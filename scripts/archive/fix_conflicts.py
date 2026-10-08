#!/usr/bin/env python3
import re

with open('pkg/infra/config.go', 'r') as f:
    content = f.read()

# Fix the first conflict in DefaultConfig (around line 525)
pattern1 = r'<<<<<<< Updated upstream\n\t\tFSRS: FSRSConfig\{.*?\n=======\n\t\tDatabase: DatabaseConfig\{\n\t\t\tPath: "", // empty → database\.Open resolves to \$HOME/\.home-tutor-agent/vocab\.db\n>>>>>> Stashed changes'
replacement1 = '''Database: DatabaseConfig{
				Path: "", // empty → database.Open resolves to $HOME/.home-tutor-agent/vocab.db
			},
			FSRS: FSRSConfig{
				// FSRS-02 default word count sits in the 20-30 band so a session
				// with no explicit count serves a sensible batch.
				DefaultWordCount: 25,
				// FSRS-03 fetch-lock: a fetched word set is fresh for 24h before a
				// re-fetch is permitted.
				FetchLockHours:  24.0,
				DefaultUnit:     "", // empty = all units (valid default)
				DefaultCategory: "english",
			},'''

content = re.sub(pattern1, replacement1, content, flags=re.DOTALL)

# Fix the second conflict in bindEnvForAllLeaves (around line 878)
pattern2 = r'<<<<<<< Updated upstream\n\t\t\t// Phase 5 FSRS orchestration \(D-03\)\. Pitfall 6: every leaf MUST be\n\t\t\t// bound explicitly or HTA_FSRS_\* env vars are silently inert\.\n\t\t\t"fsrs\.defaultWordCount",\n\t\t\t"fsrs\.fetchLockHours",\n\t\t\t"fsrs\.defaultUnit",\n\t\t\t"fsrs\.defaultCategory",\n=======\n\t\t\t"database\.path",\n>>>>>> Stashed changes'
replacement2 = '''// Phase 5 FSRS orchestration (D-03). Pitfall 6: every leaf MUST be
			// bound explicitly or HTA_FSRS_* env vars are silently inert.
			"fsrs.defaultWordCount",
			"fsrs.fetchLockHours",
			"fsrs.defaultUnit",
			"fsrs.defaultCategory",
			// Database path (Phase 4).
			"database.path",'''

content = re.sub(pattern2, replacement2, content, flags=re.DOTALL)

with open('pkg/infra/config.go', 'w') as f:
    f.write(content)

print("Fixed merge conflicts in config.go")
