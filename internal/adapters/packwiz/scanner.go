package packwiz

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/managedfiles"
	"packgradle/internal/application/policy"
	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// 哨兵错误：application 映射为 err.* 结构化错误。
var (
	ErrNotPackwizProject = errors.New("packwiz: 缺少 pack.toml（不是 Packwiz 项目）")
	ErrIndexMissing      = errors.New("packwiz: 缺少 index.toml（权威列表不可用）")
)

// Scanner 实现 ports.ProjectScanner。
type Scanner struct{}

// New 构造 Packwiz 项目扫描器。
func New() *Scanner { return &Scanner{} }

// Name 返回扫描器名。
func (s *Scanner) Name() string { return "packwiz" }

// Version 返回扫描器实现版本（语义变化时递增，参与快照记录但不参与 digest）。
// 1.1.0（票 #63/#59）：mod 观察新增 cf_file_id 元数据（CF 免钥匙直链取数的
// 文件编号，供 sync 计划物化模式推导与 restore 重取判定消费；不进语义摘要
// 与快照 digest）。
// 1.2.0（票 #88）：mod 观察新增 metafile 自身实测 Content（sha256+size，
// HashFile 闭包实测；对象本体在提交收口期统一入 CAS，扫描期零对象——
// ADR-0012 §2）。
func (s *Scanner) Version() string { return "1.2.0" }

// Scan 扫描项目端点：index.toml 权威 mod 列表 + MappingPolicy 受管文件规则。
// 全部端点内路径访问经 Resolver（realpath + root containment）强制入口；
// 映射策略先经 policy.Compile（编译期校验 + glob 编译 + 决议器），编译失败即错误。
func (s *Scanner) Scan(ctx context.Context, root string, opts ports.ScanOptions) (model.ScanReport, error) {
	report := model.ScanReport{}

	compiled, cerr := policy.Compile(opts.Policy)
	if cerr != nil {
		return report, fmt.Errorf("packwiz: 映射策略编译失败: %w", cerr)
	}

	rslv, err := filesystem.NewResolver(root)
	if err != nil {
		return report, fmt.Errorf("packwiz: 端点根不可达: %w", err)
	}
	packPath, err := rslv.Resolve("pack.toml")
	if err != nil {
		return report, fmt.Errorf("packwiz: pack.toml 解析失败: %w", err)
	}
	if _, err := os.Stat(packPath); err != nil {
		return report, ErrNotPackwizProject
	}
	idxPath, err := rslv.Resolve("index.toml")
	if err != nil {
		return report, fmt.Errorf("packwiz: index.toml 解析失败: %w", err)
	}
	idx, err := parseIndex(idxPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return report, ErrIndexMissing
		}
		return report, fmt.Errorf("packwiz: index.toml 解析失败: %w", err)
	}

	obs := make([]model.ResourceObservation, 0, len(idx.Files))
	modPolicyID := compiled.ModRuleID()

	for _, entry := range idx.Files {
		if !isModMetafile(entry) {
			// 包内追踪但不在受管范围（非 mods/ metafile）：标记 ignored（证据性诊断，
			// roadmap Step 4.3「扫描受 MappingPolicy 约束，明确标记 ignored」）
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "info", Code: "diag.scan.ignored",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: "index.toml 条目不在受管范围（非 mods/ metafile），已忽略",
			})
			continue
		}
		relLower, err := normalize.NormalizeRelativePath(entry.File, true)
		if err != nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: "index.toml 条目路径非法（绝对路径或 .. 穿越），已忽略",
			})
			continue
		}
		absMeta, rerr := rslv.Resolve(entry.File)
		if rerr != nil {
			// 条目解析后越出端点根目录（symlink/junction 指向 root 外）：整条跳过
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: "index.toml 条目解析后越出端点根目录，已忽略: " + rerr.Error(),
			})
			continue
		}
		// metafile 实测摘要捕获（ADR-0012 §2）：Content 由 application 注入的
		// HashFile 闭包（含 hash cache）实测 sha256+size，落表示 Content；对象
		// 本体在提交收口期按 digest 统一从工作树读字节入 CAS，扫描期零 CAS
		// 对象。缺 hasher/哈希失败按 managedfiles 同款诊断语义落警告诊断：
		// 观察保留（metafile 的语义身份是同步面权威），表示退无 Content（回滚
		// 面按无源行自然落降级语义）。
		var metaContent *model.ContentRef
		if opts.HashFile == nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hasher_missing",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: "扫描选项未注入哈希函数，metafile 未捕获内容摘要",
			})
		} else if c, _, herr := opts.HashFile(ctx, absMeta); herr != nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hash_failed",
				Args: []string{entry.File}, RelativePath: entry.File, Detail: herr.Error(),
			})
		} else {
			c := c
			metaContent = &c
		}
		meta, metaErr := parseModMeta(absMeta)
		if metaErr != nil {
			// 容错哲学：条目保留（低置信度路径身份），错误落诊断
			obs = append(obs, model.ResourceObservation{
				ResourceID: model.ResourceID("mod:path:" + relLower),
				Kind:       model.ResourceMod,
				Identity:   model.Identity{Provider: "path", Key: relLower, Confidence: model.ConfidenceLow},
				Representation: model.Representation{
					RelativePath: entry.File,
					Format:       "packwiz-mod-toml",
					Content:      metaContent,
				},
				PolicyID: modPolicyID,
			})
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.modmeta_unreadable",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: metaErr.Error(),
			})
			continue
		}

		id, identity := modIdentity(meta, relLower)
		metadata := map[string]string{
			model.MetaDisplayName: meta.Name,
		}
		if v := modVersion(meta); v != "" {
			metadata[model.MetaVersion] = v
		}
		if side := normalize.NormalizeSide(meta.Side); side != "" {
			metadata[model.MetaSide] = side
		}
		if meta.Download.HashFormat != "" && meta.Download.Hash != "" {
			metadata[model.MetaDeclaredHashAlgo] = strings.ToLower(meta.Download.HashFormat)
			metadata[model.MetaDeclaredHashValue] = meta.Download.Hash
		}
		// update.curseforge.file-id：CF 免钥匙直链取数信息（票 #63 sync 物化
		// 模式推导 + 票 #59 restore 重取判定共用同一键）。缺失或非数值不落键
		// ——物化模式推导把「无 file-id」视作无重取信息（copy），restore 侧
		// 同键观察「看数据不看出身」。
		if cf, ok := meta.Update["curseforge"]; ok {
			if fid := anyToString(cf["file-id"]); fid != "" {
				metadata[model.MetaCFFileID] = fid
			}
		}
		if meta.Filename != "" {
			metadata[model.MetaFilename] = meta.Filename
		}

		obs = append(obs, model.ResourceObservation{
			ResourceID: id,
			Kind:       model.ResourceMod,
			Identity:   identity,
			Representation: model.Representation{
				RelativePath: entry.File,
				Format:       "packwiz-mod-toml",
				Content:      metaContent,
				Metadata:     metadata,
			},
			PolicyID: modPolicyID,
		})
	}

	// 受管文件规则（text_file/binary_file）：编译产物驱动，含碰撞决议
	fileObs, fileDiags, err := managedfiles.Scan(ctx, rslv, compiled, model.SideProject, opts)
	if err != nil {
		return report, err
	}
	obs = append(obs, fileObs...)
	report.Diagnostics = append(report.Diagnostics, fileDiags...)

	sort.Slice(obs, func(i, j int) bool { return obs[i].ResourceID < obs[j].ResourceID })
	report.Observations = obs
	return report, nil
}

