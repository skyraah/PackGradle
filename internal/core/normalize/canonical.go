package normalize

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"packgradle/internal/core/model"
)

// CanonicalJSON 递归编码为确定性 JSON：map key 按 UTF-8 字节序排序、
// 不做 HTML 转义、整数用十进制、禁止浮点（遇到即报错）。
// 禁止直接对 Go map 的 encoding/json 序列化结果做 hash（架构文档 §6.2.1 第 5 条）。
// 支持的类型：nil、bool、string、整数、[]any、map[string]any。
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest 对 canonical 对象计算 "sha256:<hex>"。
func Digest(v any) (string, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func encodeValue(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		encodeString(buf, t)
	case int:
		buf.WriteString(strconv.Itoa(t))
	case int8:
		buf.WriteString(strconv.FormatInt(int64(t), 10))
	case int16:
		buf.WriteString(strconv.FormatInt(int64(t), 10))
	case int32:
		buf.WriteString(strconv.FormatInt(int64(t), 10))
	case int64:
		buf.WriteString(strconv.FormatInt(t, 10))
	case uint:
		buf.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint8:
		buf.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint16:
		buf.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint32:
		buf.WriteString(strconv.FormatUint(uint64(t), 10))
	case uint64:
		buf.WriteString(strconv.FormatUint(t, 10))
	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, k)
			buf.WriteByte(':')
			if err := encodeValue(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical: 不支持的类型 %T（禁止浮点与非规范对象）", v)
	}
	return nil
}

// encodeString 做 JSON 字符串转义：控制字符、引号、反斜杠；
// 不做 HTML 转义（<>& 原样输出）。
func encodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			buf.WriteString(`\"`)
		case c == '\\':
			buf.WriteString(`\\`)
		case c == '\n':
			buf.WriteString(`\n`)
		case c == '\r':
			buf.WriteString(`\r`)
		case c == '\t':
			buf.WriteString(`\t`)
		case c < 0x20:
			fmt.Fprintf(buf, `\u%04x`, c)
		default:
			buf.WriteByte(c)
		}
	}
	buf.WriteByte('"')
}

// SnapshotDigest 计算受管逻辑内容 revision（架构文档 §6.2.1）。
//
// 包含：normalization_version、side、policy_digest、按 resource_id 排序的资源表
// （kind/identity/规范化路径/format/semantic_digest）。
// 排除：snapshot_id、relation_id、captured_at、binding_fingerprint（绑定证据与内容
// revision 用途不同）、scanner name/version、diagnostics、绝对路径、缓存命中信息。
func SnapshotDigest(s model.ObservedSnapshot) (string, error) {
	resources := make(map[string]any, len(s.Resources))
	for id, obs := range s.Resources {
		sem, err := SemanticDigest(obs.Kind, obs.Representation, obs.Identity)
		if err != nil {
			return "", fmt.Errorf("resource %s: %w", id, err)
		}
		path, err := NormalizeRelativePath(obs.Representation.RelativePath, true)
		if err != nil {
			return "", fmt.Errorf("resource %s: %w", id, err)
		}
		resources[string(id)] = map[string]any{
			"kind":     string(obs.Kind),
			"identity": identityObject(obs.Identity),
			"representation": map[string]any{
				"relative_path":   path,
				"format":          obs.Representation.Format,
				"semantic_digest": sem,
			},
		}
	}
	return Digest(map[string]any{
		"normalization_version": s.NormalizationVersion,
		"side":                  string(s.Side),
		"policy_digest":         s.PolicyDigest,
		"resources":             resources,
	})
}

// BaselineDigest 计算基线内容摘要。
// 排除 baseline_id、parent_baseline_id、relation_id、created_at。
func BaselineDigest(b model.SyncBaseline) (string, error) {
	resources := make(map[string]any, len(b.Resources))
	for id, res := range b.Resources {
		if res.State == "absent" {
			resources[string(id)] = map[string]any{"state": "absent"}
			continue
		}
		projectObj, err := baselineSideObject(id, res.ProjectRepresentation)
		if err != nil {
			return "", fmt.Errorf("baseline %s resource %s: %w", b.BaselineID, id, err)
		}
		runtimeObj, err := baselineSideObject(id, res.RuntimeRepresentation)
		if err != nil {
			return "", fmt.Errorf("baseline %s resource %s: %w", b.BaselineID, id, err)
		}
		resources[string(id)] = map[string]any{
			"state":          res.State,
			"logical_digest": res.LogicalDigest,
			"project":        projectObj,
			"runtime":        runtimeObj,
			"recoverability": string(res.Recoverability),
		}
	}
	return Digest(map[string]any{
		"normalization_version": b.NormalizationVersion,
		"resources":             resources,
	})
}

func baselineSideObject(id model.ResourceID, rep *model.Representation) (any, error) {
	if rep == nil {
		return nil, nil
	}
	sem, err := SemanticDigest(KindOfResourceID(id), *rep, IdentityFromResourceID(id))
	if err != nil {
		return nil, err
	}
	path, err := NormalizeRelativePath(rep.RelativePath, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"relative_path":   path,
		"format":          rep.Format,
		"semantic_digest": sem,
	}, nil
}

// PlanDigest 计算计划摘要。
// 包含：normalization_version、relation_revision、kind、resolved、
// base_baseline_digest、输入快照 digest、policy_digest、expected_bindings、
// 确定性排序后的 operations、最小化 conflicts（resource_id+kind）、resolutions。
// 排除：plan_id、relation_id、resolved_from_plan_id、status、expires_at、
// confirmation_requirements 与 summary（均可由上述字段推导）。
func PlanDigest(p model.SyncPlan) (string, error) {
	ops := make([]any, 0, len(p.Operations))
	for _, op := range p.Operations {
		preconds := make([]any, 0, len(op.Preconditions))
		for _, pc := range op.Preconditions {
			obj := map[string]any{
				"resource_id": string(pc.ResourceID),
				"side":        pc.Side,
				"existence":   pc.Existence,
			}
			if pc.Expected != nil {
				obj["expected"] = contentObject(*pc.Expected)
			}
			preconds = append(preconds, obj)
		}
		ops = append(ops, map[string]any{
			"id":            op.ID,
			"kind":          string(op.Kind),
			"resource_id":   string(op.ResourceID),
			"preconditions": preconds,
			"reversible":    op.Reversible,
		})
	}
	conflicts := make([]any, 0, len(p.Conflicts))
	for _, c := range p.Conflicts {
		conflicts = append(conflicts, map[string]any{
			"resource_id": string(c.ResourceID),
			"kind":        string(c.Kind),
		})
	}
	resolutions := make([]any, 0, len(p.Resolutions))
	for _, r := range p.Resolutions {
		resolutions = append(resolutions, map[string]any{
			"resource_id": string(r.ResourceID),
			"choice":      string(r.Choice),
		})
	}
	var baseDigest any
	if p.BaseBaselineDigest != "" {
		baseDigest = p.BaseBaselineDigest
	}
	resolved := len(p.Resolutions) > 0 || p.ResolvedFromPlanID != ""
	return Digest(map[string]any{
		"normalization_version":         normalizeVersionConst(),
		"relation_revision":             p.RelationRevision,
		"kind":                          string(p.Kind),
		"resolved":                      resolved,
		"base_baseline_digest":          baseDigest,
		"input_project_snapshot_digest": p.InputProjectSnapshotDigest,
		"input_runtime_snapshot_digest": p.InputRuntimeSnapshotDigest,
		"policy_digest":                 p.PolicyDigest,
		"expected_bindings": map[string]any{
			"project": p.ExpectedBindings.Project,
			"runtime": p.ExpectedBindings.Runtime,
		},
		"operations":  ops,
		"conflicts":   conflicts,
		"resolutions": resolutions,
	})
}

func normalizeVersionConst() int { return NormalizationVersion }

// PolicyDigest 计算 MappingPolicy 的 canonical 摘要。
// 规则按 ID 排序；include/exclude 排序后编码（集合语义无序）。
func PolicyDigest(p model.MappingPolicy) (string, error) {
	rules := make([]any, 0, len(p.Rules))
	sorted := make([]model.MappingRule, len(p.Rules))
	copy(sorted, p.Rules)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, r := range sorted {
		rules = append(rules, map[string]any{
			"id":              r.ID,
			"resource_kind":   r.ResourceKind,
			"project_prefix":  strings.ToLower(r.ProjectPrefix),
			"runtime_prefix":  strings.ToLower(r.RuntimePrefix),
			"include":         sortedStrings(r.Include),
			"exclude":         sortedStrings(r.Exclude),
			"direction":       r.Direction,
			"materialization": r.Materialization,
			"merge_policy":    r.MergePolicy,
			"runtime_local":   r.RuntimeLocalPolicy,
		})
	}
	return Digest(map[string]any{
		"schema_version": p.SchemaVersion,
		"policy_id":      p.PolicyID,
		"revision":       p.Revision,
		"rules":          rules,
	})
}

func sortedStrings(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].(string) < out[j].(string)
	})
	return out
}

// absentTombstoneDigest 是 {"state":"absent"} 的固定摘要（显式 tombstone，
// 架构文档 §6.2.1 第 7 条：不能以缺行和空 digest 混用）。
var absentTombstoneDigest = func() string {
	return sha256Hex([]byte(`{"state":"absent"}`))
}()

// AbsentTombstone 返回显式 absent 状态的固定 digest。
func AbsentTombstone() string { return absentTombstoneDigest }
