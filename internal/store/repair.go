package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Crash repair inspection and outcome status values.
const (
	RepairStatusConsistent = "consistent"
	RepairStatusRepairable = "repairable"
	RepairStatusRepaired   = "repaired"
	RepairStatusMissing    = "missing"
	RepairStatusCorrupt    = "corrupt"
	RepairStatusAmbiguous  = "ambiguous"
)

// ErrRepairSelectionRequired is returned when a non-dry-run repair call omits both explicit crash IDs and All=true.
var ErrRepairSelectionRequired = errors.New("explicit crash IDs or --all is required when applying repairs")

// CrashRepairOptions configures Crash reproducer inspection and repair.
type CrashRepairOptions struct {
	CrashIDs  []int64 `json:"crash_ids,omitempty"`
	All       bool    `json:"all,omitempty"`
	DryRun    bool    `json:"dry_run"`
	CorpusDir string  `json:"corpus_dir,omitempty"`
}

// CrashRepairCandidate describes a candidate reproducer source inspected for a crash record.
type CrashRepairCandidate struct {
	Role        string     `json:"role"` // "stored_input_bytes", "minimized_input_file", "recorded_hash_file"
	Path        string     `json:"path,omitempty"`
	ContentHash string     `json:"content_hash,omitempty"`
	SizeBytes   int        `json:"size_bytes,omitempty"`
	ModTime     *time.Time `json:"mod_time,omitempty"`
	ValidFormat bool       `json:"valid_format"`
	MatchesDB   bool       `json:"matches_db"`
	Preview     string     `json:"preview,omitempty"`
}

// CrashRepairAction reports the inspection findings and proposed or applied changes for a single crash record.
type CrashRepairAction struct {
	CrashID           int64                  `json:"crash_id"`
	TargetID          int64                  `json:"target_id"`
	ProjectName       string                 `json:"project_name,omitempty"`
	Package           string                 `json:"package,omitempty"`
	TargetName        string                 `json:"target_name,omitempty"`
	GitSHA            string                 `json:"git_sha,omitempty"`
	CurrentProjectSHA string                 `json:"current_project_sha,omitempty"`
	FirstSeen         time.Time              `json:"first_seen"`
	LastSeen          time.Time              `json:"last_seen"`
	FixedAt           *time.Time             `json:"fixed_at,omitempty"`
	IssueURL          string                 `json:"issue_url,omitempty"`
	StackFingerprint  string                 `json:"stack_fingerprint,omitempty"`
	Status            string                 `json:"status"`
	Reason            string                 `json:"reason"`
	OldInputHash      string                 `json:"old_input_hash"`
	NewInputHash      string                 `json:"new_input_hash"`
	OldMinimizedInput string                 `json:"old_minimized_input"`
	NewMinimizedInput string                 `json:"new_minimized_input"`
	OldInputBytesLen  int                    `json:"old_input_bytes_len"`
	NewInputBytesLen  int                    `json:"new_input_bytes_len"`
	OldInputTruncated bool                   `json:"old_input_truncated"`
	NewInputTruncated bool                   `json:"new_input_truncated"`
	ReplayEligible    bool                   `json:"replay_eligible"`
	Candidates        []CrashRepairCandidate `json:"candidates,omitempty"`

	newBytes []byte
}

// CrashRepairReport summarizes a dry-run or applied repair pass across selected crashes.
type CrashRepairReport struct {
	DryRun  bool                `json:"dry_run"`
	Actions []CrashRepairAction `json:"actions"`
}

type targetHashKey struct {
	targetID  int64
	inputHash string
}

// RepairCrashes inspects selected crash records for reproducer metadata mismatches,
// verifies candidate corpus files and their provenance, and (when opts.DryRun is false)
// atomically updates verified repairable records while preserving crash IDs, reporting
// metadata, and historical replay rows.
func (s *Store) RepairCrashes(ctx context.Context, opts CrashRepairOptions) (CrashRepairReport, error) {
	if s.readonly != nil && !opts.DryRun {
		return CrashRepairReport{}, ErrReadOnly
	}
	if !opts.DryRun && len(opts.CrashIDs) == 0 && !opts.All {
		return CrashRepairReport{}, ErrRepairSelectionRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	crashes, err := s.loadCrashesForRepairLocked(ctx, opts.CrashIDs)
	if err != nil {
		return CrashRepairReport{}, err
	}

	ownerByTargetHash, err := s.loadTargetInputHashesLocked(ctx)
	if err != nil {
		return CrashRepairReport{}, err
	}

	projectSHAByTarget, err := s.loadProjectSHAByTargetLocked(ctx)
	if err != nil {
		return CrashRepairReport{}, err
	}

	report := CrashRepairReport{
		DryRun:  opts.DryRun,
		Actions: make([]CrashRepairAction, 0, len(crashes)),
	}

	for _, c := range crashes {
		currentProjSHA := projectSHAByTarget[c.TargetID]
		act := inspectCrashForRepair(c, currentProjSHA, opts.CorpusDir, ownerByTargetHash)
		if act.Status == RepairStatusRepairable {
			// Reserve the proposed hash in-batch so two crashes for the same target cannot collide.
			ownerByTargetHash[targetHashKey{targetID: c.TargetID, inputHash: act.NewInputHash}] = c.ID
		}
		if !opts.DryRun && act.Status == RepairStatusRepairable {
			if err := s.applyCrashRepairLocked(ctx, act); err != nil {
				return CrashRepairReport{}, err
			}
			act.Status = RepairStatusRepaired
		}
		act.ReplayEligible = s.isReplayEligibleLocked(ctx, c.ID, c.GitSHA, currentProjSHA, act.NewInputHash, act.Status == RepairStatusRepairable || act.Status == RepairStatusRepaired)
		report.Actions = append(report.Actions, act)
	}

	return report, nil
}

func (s *Store) loadCrashesForRepairLocked(ctx context.Context, crashIDs []int64) ([]Crash, error) {
	if len(crashIDs) > 0 {
		seen := make(map[int64]bool, len(crashIDs))
		out := make([]Crash, 0, len(crashIDs))
		for _, id := range crashIDs {
			if id <= 0 {
				return nil, fmt.Errorf("invalid crash id %d", id)
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			row := s.queryRow(ctx, s.crashSelectSQL(ctx)+` WHERE c.id = ?`, id)
			c, err := scanCrashRow(row)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("crash %d: %w", id, ErrNotFound)
			}
			if err != nil {
				return nil, fmt.Errorf("query crash %d: %w", id, err)
			}
			out = append(out, c)
		}
		return out, nil
	}

	rows, err := s.query(ctx, s.crashSelectSQL(ctx)+` ORDER BY c.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list crashes for repair: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Crash
	for rows.Next() {
		c, err := scanCrashRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan crash for repair: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) loadTargetInputHashesLocked(ctx context.Context) (map[targetHashKey]int64, error) {
	rows, err := s.query(ctx, `SELECT id, target_id, COALESCE(input_hash, '') FROM crashes ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	owners := make(map[targetHashKey]int64)
	for rows.Next() {
		var id, targetID int64
		var hash string
		if err := rows.Scan(&id, &targetID, &hash); err != nil {
			return nil, err
		}
		if hash != "" {
			k := targetHashKey{targetID: targetID, inputHash: hash}
			if _, exists := owners[k]; !exists {
				owners[k] = id
			}
		}
	}
	return owners, rows.Err()
}

func (s *Store) loadProjectSHAByTargetLocked(ctx context.Context) (map[int64]string, error) {
	rows, err := s.query(ctx, `
		SELECT t.id, COALESCE(p.git_sha, '')
		FROM targets t
		JOIN projects p ON p.id = t.project_id
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]string)
	for rows.Next() {
		var targetID int64
		var sha string
		if err := rows.Scan(&targetID, &sha); err != nil {
			return nil, err
		}
		out[targetID] = sha
	}
	return out, rows.Err()
}

func (s *Store) applyCrashRepairLocked(ctx context.Context, act CrashRepairAction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	inputHex := hex.EncodeToString(act.newBytes)
	res, err := tx.ExecContext(ctx,
		`UPDATE crashes
		 SET input_hash = ?,
		     minimized_input = ?,
		     input_bytes = CASE WHEN ? != '' THEN unhex(?) ELSE NULL END,
		     input_truncated = ?
		 WHERE id = ? AND input_hash = ?`,
		act.NewInputHash,
		act.NewMinimizedInput,
		inputHex, inputHex,
		act.NewInputTruncated,
		act.CrashID,
		act.OldInputHash,
	)
	if err != nil {
		return fmt.Errorf("repair crash %d: %w", act.CrashID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("repair crash %d: concurrent modification or record not found", act.CrashID)
	}

	// Ensure any prior blocked replay entry for the repaired hash is immediately eligible
	// for retry on the next orchestrator pass without deleting historical replay rows.
	_, err = tx.ExecContext(ctx,
		`UPDATE crash_replays
		 SET checked_at = LEAST(checked_at, (CURRENT_TIMESTAMP AT TIME ZONE 'UTC') - INTERVAL '61 minutes')
		 WHERE crash_id = ? AND input_hash = ? AND outcome = 'blocked'`,
		act.CrashID,
		act.NewInputHash,
	)
	if err != nil {
		return fmt.Errorf("reset blocked replay cooldown for crash %d: %w", act.CrashID, err)
	}

	return tx.Commit()
}

func (s *Store) isReplayEligibleLocked(ctx context.Context, crashID int64, crashSHA, currentProjSHA, inputHash string, coolingReset bool) bool {
	if currentProjSHA == "" || inputHash == "" || crashSHA == currentProjSHA {
		return false
	}
	if coolingReset {
		var nonBlocked int
		err := s.queryRow(ctx,
			`SELECT COUNT(*) FROM crash_replays WHERE crash_id = ? AND git_sha = ? AND input_hash = ? AND outcome <> 'blocked'`,
			crashID, currentProjSHA, inputHash,
		).Scan(&nonBlocked)
		return err == nil && nonBlocked == 0
	}
	var count int
	err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM crash_replays WHERE crash_id = ? AND git_sha = ? AND input_hash = ? AND (outcome <> 'blocked' OR checked_at > (CURRENT_TIMESTAMP AT TIME ZONE 'UTC') - INTERVAL '1 hour')`,
		crashID, currentProjSHA, inputHash,
	).Scan(&count)
	return err == nil && count == 0
}

func inspectCrashForRepair(c Crash, currentProjSHA, corpusDirOverride string, ownerByTargetHash map[targetHashKey]int64) CrashRepairAction {
	act := CrashRepairAction{
		CrashID:           c.ID,
		TargetID:          c.TargetID,
		ProjectName:       c.ProjectName,
		Package:           c.Package,
		TargetName:        c.TargetName,
		GitSHA:            c.GitSHA,
		CurrentProjectSHA: currentProjSHA,
		FirstSeen:         c.FirstSeen,
		LastSeen:          c.LastSeen,
		FixedAt:           c.FixedAt,
		IssueURL:          c.IssueURL,
		StackFingerprint:  c.StackFingerprint,
		OldInputHash:      c.InputHash,
		NewInputHash:      c.InputHash,
		OldMinimizedInput: c.MinimizedInput,
		NewMinimizedInput: c.MinimizedInput,
		OldInputBytesLen:  len(c.InputBytes),
		NewInputBytesLen:  len(c.InputBytes),
		OldInputTruncated: c.InputTruncated,
		NewInputTruncated: c.InputTruncated,
		newBytes:          c.InputBytes,
	}

	hasDBBytes := len(c.InputBytes) > 0
	dbBytesValid := hasDBBytes && isValidGoFuzzCorpus(c.InputBytes)
	dbBytesHash := ""
	if hasDBBytes && !c.InputTruncated {
		dbBytesHash = HashCrashInput(c.InputBytes)
	}
	if hasDBBytes {
		act.Candidates = append(act.Candidates, CrashRepairCandidate{
			Role:        "stored_input_bytes",
			ContentHash: dbBytesHash,
			SizeBytes:   len(c.InputBytes),
			ValidFormat: dbBytesValid,
			MatchesDB:   true,
			Preview:     summarizeCorpusPreview(c.InputBytes),
		})
	}

	minPath := strings.TrimSpace(c.MinimizedInput)
	hasMinPath := minPath != "" && minPath != "inline-failure"
	pathTraversal := hasMinPath && hasTraversalSegment(minPath)
	inExpectedTargetDir := hasMinPath && !pathTraversal && isExpectedTargetCrashPath(minPath, c.Package, c.TargetName)

	resolvedMinPath := resolveCorpusDiskPath(minPath, corpusDirOverride, c.ProjectName, c.Package, c.TargetName)
	var minFileExists bool
	var minFileBytes []byte
	var minFileHash string
	var minFileValid bool
	var minFileModTime time.Time
	var minFileMatchesDB bool
	var cappedMinBytes []byte
	var cappedMinTrunc bool

	if hasMinPath && !pathTraversal && resolvedMinPath != "" {
		if info, err := os.Lstat(resolvedMinPath); err == nil && info.Mode().IsRegular() {
			if raw, readErr := os.ReadFile(resolvedMinPath); readErr == nil {
				minFileExists = true
				minFileBytes = raw
				minFileModTime = info.ModTime().UTC()
				if len(raw) > 0 {
					minFileHash = HashCrashInput(raw)
				}
				minFileValid = isValidGoFuzzCorpus(raw)
				cappedMinBytes, cappedMinTrunc = capBytesForStore(raw)
				minFileMatchesDB = hasDBBytes && bytes.Equal(c.InputBytes, cappedMinBytes) && c.InputTruncated == cappedMinTrunc
				mt := minFileModTime
				act.Candidates = append(act.Candidates, CrashRepairCandidate{
					Role:        "minimized_input_file",
					Path:        minPath,
					ContentHash: minFileHash,
					SizeBytes:   len(raw),
					ModTime:     &mt,
					ValidFormat: minFileValid,
					MatchesDB:   minFileMatchesDB,
					Preview:     summarizeCorpusPreview(raw),
				})
			}
		}
	}

	var origFileExists bool
	var origFileHash string
	var origFileValid bool
	var origFileModTime time.Time
	var origFileMatchesDB bool
	var origDisplayPath string

	if hasMinPath && !pathTraversal && c.InputHash != "" && filepath.Base(minPath) != c.InputHash && !hasTraversalSegment(c.InputHash) {
		origDisplayPath = filepath.Join(filepath.Dir(minPath), filepath.Base(c.InputHash))
		origDiskPath := origDisplayPath
		if resolvedMinPath != "" {
			origDiskPath = filepath.Join(filepath.Dir(resolvedMinPath), filepath.Base(c.InputHash))
		}
		if info, err := os.Lstat(origDiskPath); err == nil && info.Mode().IsRegular() {
			if raw, readErr := os.ReadFile(origDiskPath); readErr == nil {
				origFileExists = true
				origFileModTime = info.ModTime().UTC()
				if len(raw) > 0 {
					origFileHash = HashCrashInput(raw)
				}
				origFileValid = isValidGoFuzzCorpus(raw)
				cappedOrig, cappedOrigTrunc := capBytesForStore(raw)
				origFileMatchesDB = hasDBBytes && bytes.Equal(c.InputBytes, cappedOrig) && c.InputTruncated == cappedOrigTrunc
				mt := origFileModTime
				act.Candidates = append(act.Candidates, CrashRepairCandidate{
					Role:        "recorded_hash_file",
					Path:        origDisplayPath,
					ContentHash: origFileHash,
					SizeBytes:   len(raw),
					ModTime:     &mt,
					ValidFormat: origFileValid,
					MatchesDB:   origFileMatchesDB,
					Preview:     summarizeCorpusPreview(raw),
				})
			}
		}
	}

	// 1. Check if the crash record is already consistent.
	if !c.InputTruncated && hasDBBytes && dbBytesValid && dbBytesHash == c.InputHash {
		if !hasMinPath {
			act.Status = RepairStatusConsistent
			act.Reason = "stored input_bytes and input_hash are consistent"
			return act
		}
		if minFileExists && minFileValid && minFileHash == c.InputHash && filepath.Base(minPath) == c.InputHash && minFileMatchesDB {
			act.Status = RepairStatusConsistent
			act.Reason = "input_hash, minimized_input corpus file, and stored input_bytes are consistent"
			return act
		}
		if !minFileExists && filepath.Base(minPath) == c.InputHash {
			act.Status = RepairStatusConsistent
			act.Reason = "stored input_bytes and input_hash are consistent (disk file not present)"
			return act
		}
	}
	if c.InputTruncated && hasDBBytes && len(c.InputBytes) == MaxCrashInputBytes && dbBytesValid &&
		minFileExists && minFileValid && len(minFileBytes) > MaxCrashInputBytes &&
		minFileHash == c.InputHash && filepath.Base(minPath) == c.InputHash && minFileMatchesDB {
		act.Status = RepairStatusConsistent
		act.Reason = "truncated input_bytes, full minimized_input corpus file, and input_hash are consistent"
		return act
	}

	// 2. Check for corrupt inputs.
	if pathTraversal {
		act.Status = RepairStatusCorrupt
		act.Reason = "minimized_input path contains directory traversal segments"
		return act
	}
	if hasDBBytes && !dbBytesValid {
		act.Status = RepairStatusCorrupt
		act.Reason = "stored input_bytes is not a valid 'go test fuzz v1' corpus entry"
		return act
	}
	if c.InputTruncated && hasDBBytes && len(c.InputBytes) != MaxCrashInputBytes {
		act.Status = RepairStatusCorrupt
		act.Reason = fmt.Sprintf("input_truncated is true but stored input_bytes length is %d (expected %d)", len(c.InputBytes), MaxCrashInputBytes)
		return act
	}
	if minFileExists {
		if !minFileValid {
			act.Status = RepairStatusCorrupt
			act.Reason = "minimized_input file on disk is not a valid 'go test fuzz v1' corpus entry"
			return act
		}
		if filepath.Base(minPath) != minFileHash {
			act.Status = RepairStatusCorrupt
			act.Reason = fmt.Sprintf("minimized_input file content hash %s does not match filename %s", minFileHash, filepath.Base(minPath))
			return act
		}
	}
	if origFileExists {
		if !origFileValid {
			act.Status = RepairStatusCorrupt
			act.Reason = fmt.Sprintf("recorded input_hash corpus file %s is not a valid 'go test fuzz v1' corpus entry", origDisplayPath)
			return act
		}
		if origFileHash != c.InputHash {
			act.Status = RepairStatusCorrupt
			act.Reason = fmt.Sprintf("recorded input_hash corpus file %s content hash %s does not match filename %s", origDisplayPath, origFileHash, c.InputHash)
			return act
		}
	}

	// 3. Check for missing inputs.
	if !minFileExists {
		if !hasDBBytes {
			act.Status = RepairStatusMissing
			act.Reason = "minimized_input corpus file is missing on disk and stored input_bytes is empty"
			return act
		}
		if c.InputTruncated {
			act.Status = RepairStatusMissing
			act.Reason = "stored input_bytes is truncated at 64 KiB and full minimized_input file is missing on disk"
			return act
		}
		if dbBytesHash != c.InputHash {
			act.Status = RepairStatusMissing
			act.Reason = "minimized_input corpus file is missing on disk; refusing to overwrite input_hash from unverified DB bytes alone"
			return act
		}
	}

	// 4. Check for ambiguous provenance.
	if !inExpectedTargetDir {
		act.Status = RepairStatusAmbiguous
		act.Reason = fmt.Sprintf("minimized_input path %q is outside the expected target crash directory for %s/%s", minPath, c.Package, c.TargetName)
		return act
	}
	if hasDBBytes && !minFileMatchesDB {
		act.Status = RepairStatusAmbiguous
		if origFileExists && origFileMatchesDB {
			act.Reason = "stored input_bytes matches original input_hash file while minimized_input points to a different corpus file"
		} else {
			act.Reason = "stored input_bytes and minimized_input corpus file on disk disagree"
		}
		return act
	}
	if !hasDBBytes && minFileHash != c.InputHash {
		act.Status = RepairStatusAmbiguous
		act.Reason = "stored input_bytes is empty while input_hash and minimized_input disagree; cannot establish deduplication provenance"
		return act
	}
	if otherID, exists := ownerByTargetHash[targetHashKey{targetID: c.TargetID, inputHash: minFileHash}]; exists && otherID != c.ID {
		act.Status = RepairStatusAmbiguous
		act.Reason = fmt.Sprintf("candidate input_hash %s is already assigned to crash #%d on target %d", minFileHash, otherID, c.TargetID)
		return act
	}
	if origFileExists && c.LastSeen.After(c.FirstSeen) && minFileModTime.Before(origFileModTime.Add(-time.Second)) {
		act.Status = RepairStatusAmbiguous
		act.Reason = fmt.Sprintf("minimized_input mtime (%s) precedes original input_hash file mtime (%s) despite last_seen > first_seen",
			minFileModTime.Format(time.RFC3339), origFileModTime.Format(time.RFC3339))
		return act
	}

	// 5. Verified repairable cases.
	if hasDBBytes && minFileMatchesDB && c.InputHash != minFileHash {
		act.Status = RepairStatusRepairable
		act.NewInputHash = minFileHash
		act.NewMinimizedInput = minPath
		act.NewInputBytesLen = len(cappedMinBytes)
		act.NewInputTruncated = cappedMinTrunc
		act.newBytes = cappedMinBytes
		if origFileExists {
			act.Reason = fmt.Sprintf("verified stack-fingerprint deduplication update: minimized_input and stored input_bytes match %s (last_seen), while input_hash retained initial %s (first_seen)", minFileHash, c.InputHash)
		} else {
			act.Reason = fmt.Sprintf("verified stack-fingerprint deduplication update: minimized_input and stored input_bytes match %s, while input_hash retained %s", minFileHash, c.InputHash)
		}
		return act
	}

	if !hasDBBytes && c.InputHash == minFileHash {
		act.Status = RepairStatusRepairable
		act.NewInputHash = minFileHash
		act.NewMinimizedInput = minPath
		act.NewInputBytesLen = len(cappedMinBytes)
		act.NewInputTruncated = cappedMinTrunc
		act.newBytes = cappedMinBytes
		act.Reason = fmt.Sprintf("verified corpus file %s matches input_hash; backfilling stored input_bytes (%d bytes)", minFileHash, len(cappedMinBytes))
		return act
	}

	act.Status = RepairStatusAmbiguous
	act.Reason = "unclassified reproducer metadata inconsistency"
	return act
}

func capBytesForStore(raw []byte) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if len(raw) > MaxCrashInputBytes {
		out := make([]byte, MaxCrashInputBytes)
		copy(out, raw[:MaxCrashInputBytes])
		return out, true
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, false
}

func isValidGoFuzzCorpus(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	s := string(data)
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return false
	}
	header := strings.TrimSuffix(s[:nl], "\r")
	if header != "go test fuzz v1" {
		return false
	}
	rest := strings.TrimSpace(s[nl+1:])
	return rest != ""
}

func summarizeCorpusPreview(data []byte) string {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= 1 {
		return strings.TrimSpace(s)
	}
	body := strings.TrimSpace(strings.Join(lines[1:], " "))
	if len(body) > 80 {
		body = body[:80] + "..."
	}
	return body
}

func sanitizeCorpusSegment(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(s)
}

func isHexDigest32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isExpectedTargetCrashPath(minInput, pkgImport, targetName string) bool {
	slashPath := filepath.ToSlash(strings.TrimSpace(minInput))
	if slashPath == "" || targetName == "" {
		return false
	}
	base := path.Base(slashPath)
	if !isHexDigest32(base) {
		return false
	}
	targetDir := path.Base(path.Dir(slashPath))
	if targetDir != targetName {
		return false
	}
	if strings.Contains(slashPath, "/testdata/fuzz/") || strings.HasPrefix(slashPath, "testdata/fuzz/") {
		return path.Base(path.Dir(path.Dir(slashPath))) == "fuzz"
	}
	if strings.Contains(slashPath, "/crashes/") || strings.HasPrefix(slashPath, "crashes/") {
		parentSeg := path.Base(path.Dir(path.Dir(slashPath)))
		if parentSeg != "crashes" && pkgImport != "" {
			expectedSeg := sanitizeCorpusSegment(pkgImport)
			if parentSeg != expectedSeg {
				return false
			}
		}
		return true
	}
	return false
}

func resolveCorpusDiskPath(minPath, corpusDirOverride, projectName, pkgImport, targetName string) string {
	minPath = strings.TrimSpace(minPath)
	if minPath == "" || minPath == "inline-failure" || hasTraversalSegment(minPath) {
		return ""
	}
	if info, err := os.Lstat(minPath); err == nil && info.Mode().IsRegular() {
		return minPath
	}
	corpusDirOverride = strings.TrimSpace(corpusDirOverride)
	if corpusDirOverride == "" {
		return minPath
	}
	base := filepath.Base(minPath)
	pkgSeg := sanitizeCorpusSegment(pkgImport)
	candidates := []string{
		filepath.Join(corpusDirOverride, projectName, "crashes", pkgSeg, targetName, base),
		filepath.Join(corpusDirOverride, "crashes", pkgSeg, targetName, base),
		filepath.Join(corpusDirOverride, base),
	}
	for _, cand := range candidates {
		if info, err := os.Lstat(cand); err == nil && info.Mode().IsRegular() {
			return cand
		}
	}
	return minPath
}
