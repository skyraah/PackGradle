// Package perffixture 生成 P1 性能基线的确定性 fixture（验收规格 §2.1）：
// 固定 seed、无网络、内容伪随机（禁止全零/全同文件，避免 hash cache 行为失真）。
// 仓库内入 git 的是生成器本身；生成产物不入 git，由 cmd/pgfixture 单命令重放。
//
// 构成（3,000 受管资源）：
//   - Project 侧：pack.toml（真实 packwiz 布局，[index] 指向 index.toml）
//   - index.toml（300 个 mod 条目）+ mods/*.pw.toml metafile
//     （混合 CurseForge/Modrinth/URL 来源）；
//   - Runtime 侧：Prism 实例（instance.cfg + minecraft/），受管文件 =
//     300 个对应 JAR（约 10% 为 5~20MB、其余 200KB~5MB）+ 2,400 个
//     config/kubejs/scripts 文件（1KB~100KB，含文本与小型二进制）。
//
// 确定性：每个文件的内容流由 (seed, 序号) 派生的独立 PRNG 驱动，同参数重放
// 产出逐字节相同的目录树（hash cache 跨重放仍可命中）。
package perffixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Options 是生成参数。Mods/TextFiles 供测试收缩规模；生产基线用默认值。
// PlainMods 是「无 CF 声明 mod」变体数量（票 #60 P3 验收夹具：metafile 只有
// name/filename/side、无 [download]/[update] 段——回滚判定面的
// user_object_required / deletion_warn 行来源）。
type Options struct {
	OutDir    string // 产物根目录（project/ 与 instance/ 写在其下）
	Seed      int64  // 全局种子
	Mods      int    // mod 数量（<=0 时取 DefaultMods）
	TextFiles int    // config/kubejs/scripts 文件数量（<=0 时取 DefaultTextFiles）
	PlainMods int    // 无 CF 声明 mod 数量（0 = 不生成）
}

// Result 是生成结果摘要。
type Result struct {
	ProjectRoot   string `json:"project_root"`
	InstanceDir   string `json:"instance_dir"`
	Files         int    `json:"files"`           // 写入的文件总数
	Bytes         int64  `json:"bytes"`           // 写入的总字节数
	ModCount      int    `json:"mod_count"`       // mod LogicalResource 数
	PlainModCount int    `json:"plain_mod_count"` // 无 CF 声明 mod 数（票 #60 变体）
	ManagedFiles  int    `json:"managed_files"`   // runtime 受管文件数（JAR + 文本/二进制）
}

// DefaultMods / DefaultTextFiles 是验收规格 §2.1 的生产规模。
const (
	DefaultMods      = 300
	DefaultTextFiles = 2400
)

// Generate 生成完整 fixture 到 opts.OutDir。可重入：已存在的文件一律覆盖。
func Generate(ctx context.Context, opts Options) (Result, error) {
	if opts.Mods <= 0 {
		opts.Mods = DefaultMods
	}
	if opts.TextFiles <= 0 {
		opts.TextFiles = DefaultTextFiles
	}
	res := Result{
		ProjectRoot:  filepath.Join(opts.OutDir, "project"),
		InstanceDir:  filepath.Join(opts.OutDir, "instance"),
		ModCount:     opts.Mods,
		ManagedFiles: opts.Mods + opts.TextFiles,
	}

	// ---- Project 侧：mods/*.pw.toml + index.toml + pack.toml ----
	var indexEntries []string
	for i := 0; i < opts.Mods; i++ {
		metaName, metaDigest, err := writeProjectMod(ctx, res.ProjectRoot, opts, i)
		if err != nil {
			return res, err
		}
		res.Files++
		indexEntries = append(indexEntries,
			fmt.Sprintf("\n[[files]]\nfile = \"mods/%s\"\nhash = \"%s\"\nmetafile = true\n", metaName, metaDigest))
	}
	// 无 CF 声明 mod 变体（票 #60）：metafile 无 [download]/[update] 段，
	// 运行端配套 JAR；回滚判定面 user_object_required/deletion_warn 行来源。
	for i := 0; i < opts.PlainMods; i++ {
		metaName, metaDigest, jarName, err := writeProjectPlainMod(ctx, res.ProjectRoot, opts, i)
		if err != nil {
			return res, err
		}
		res.Files++
		indexEntries = append(indexEntries,
			fmt.Sprintf("\n[[files]]\nfile = \"mods/%s\"\nhash = \"%s\"\nmetafile = true\n", metaName, metaDigest))
		jarPath := filepath.Join(res.InstanceDir, "minecraft", "mods", jarName)
		if _, err := writeRandomFile(ctx, jarPath, fileSeed(opts.Seed, 4_000_000+uint64(i)), jarSize(i+opts.Mods)); err != nil {
			return res, err
		}
		res.Files++
	}
	res.PlainModCount = opts.PlainMods
	res.ManagedFiles += opts.PlainMods
	indexToml := "hash-format = \"sha256\"\n" + strings.Join(indexEntries, "")
	if _, err := writeFile(ctx, filepath.Join(res.ProjectRoot, "index.toml"), indexToml); err != nil {
		return res, err
	}
	res.Files++
	indexHash, err := fileDigest(filepath.Join(res.ProjectRoot, "index.toml"))
	if err != nil {
		return res, err
	}
	packToml := fmt.Sprintf("name = \"Perf Fixture\"\nauthor = \"pgfixture\"\nversion = \"1.0.0\"\n\n[index]\nfile = \"index.toml\"\nhash-format = \"sha256\"\nhash = \"%s\"\n", indexHash)
	if _, err := writeFile(ctx, filepath.Join(res.ProjectRoot, "pack.toml"), packToml); err != nil {
		return res, err
	}
	res.Files++

	// ---- Runtime 侧：instance.cfg + minecraft/mods/*.jar ----
	instCfg := "[General]\nname=\"Perf Fixture\"\niconKey=default\n"
	if _, err := writeFile(ctx, filepath.Join(res.InstanceDir, "instance.cfg"), instCfg); err != nil {
		return res, err
	}
	res.Files++
	gameDir := filepath.Join(res.InstanceDir, "minecraft")
	for i := 0; i < opts.Mods; i++ {
		jarPath := filepath.Join(gameDir, "mods", jarFileName(i))
		if _, err := writeRandomFile(ctx, jarPath, fileSeed(opts.Seed, 1_000_000+uint64(i)), jarSize(i)); err != nil {
			return res, err
		}
		res.Files++
	}

	// ---- Runtime 侧：config/kubejs/scripts 受管文件 ----
	for i := 0; i < opts.TextFiles; i++ {
		rel := textRelPath(i)
		if _, err := writeRandomFile(ctx, filepath.Join(gameDir, filepath.FromSlash(rel)),
			fileSeed(opts.Seed, 2_000_000+uint64(i)), textSize(i)); err != nil {
			return res, err
		}
		res.Files++
	}
	_ = filepath.WalkDir(opts.OutDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				res.Bytes += info.Size()
			}
		}
		return nil
	})
	return res, nil
}

// fileSeed 把 (全局种子, 文件序号) 混合为独立文件种子（splitmix64 终混）。
func fileSeed(seed int64, index uint64) uint64 {
	x := uint64(seed) ^ (index * 0x9E3779B97F4A7C15)
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return x
}

// prng 是 splitmix64 伪随机流（确定性、无锁、无共享状态）。
type prng struct{ state uint64 }

func (p *prng) next() uint64 {
	p.state += 0x9E3779B97F4A7C15
	z := p.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// fill 用伪随机字节填满 buf（splitmix64 高位熵充分，杜绝全零/全同）。
func (p *prng) fill(buf []byte) {
	for i := 0; i+8 <= len(buf); i += 8 {
		v := p.next()
		buf[i] = byte(v)
		buf[i+1] = byte(v >> 8)
		buf[i+2] = byte(v >> 16)
		buf[i+3] = byte(v >> 24)
		buf[i+4] = byte(v >> 32)
		buf[i+5] = byte(v >> 40)
		buf[i+6] = byte(v >> 48)
		buf[i+7] = byte(v >> 56)
	}
	for i := len(buf) - len(buf)%8; i < len(buf); i++ {
		buf[i] = byte(p.next())
	}
}

// jarFileName 返回第 i 个 mod 的 JAR 文件名（与项目侧 metafile filename 一致，
// 供跨侧身份 hint 匹配）。
func jarFileName(i int) string {
	return fmt.Sprintf("fixture-mod-%04d-1.2.%d.jar", i, i%9+1)
}

// jarSize 返回第 i 个 JAR 的大小：i%10==0 → 5~20MB（约 10%），其余 200KB~5MB。
func jarSize(i int) int64 {
	rng := prng{state: fileSeed(0x5EED, uint64(i))}
	if i%10 == 0 {
		return 5<<20 + int64(rng.next()%uint64(15<<20))
	}
	return 200<<10 + int64(rng.next()%uint64(5<<20-200<<10))
}

// metaFileName 返回第 i 个 mod 的 packwiz metafile 文件名。
func metaFileName(i int) string {
	return fmt.Sprintf("fixture-mod-%04d.pw.toml", i)
}

// writeProjectMod 写入第 i 个 mod 的 metafile，来源按 i%3 轮换
// modrinth / curseforge / url（§2.1 混合来源）；声明 hash 为 JAR 种子派生的
// 确定性占位摘要（扫描器只记录不校验）。返回 metafile 文件名与其内容摘要
//（index.toml 条目的 hash 字段）。
func writeProjectMod(ctx context.Context, projectRoot string, opts Options, i int) (metaName, metaDigest string, err error) {
	jarName := jarFileName(i)
	jarDigest := fmt.Sprintf("%064x", fileSeed(opts.Seed, 1_000_000+uint64(i)))
	rng := prng{state: fileSeed(opts.Seed, 3_000_000+uint64(i))}
	side := []string{"both", "client", "server"}[rng.next()%3]

	var content string
	switch i % 3 {
	case 0: // modrinth
		content = fmt.Sprintf("name = \"Fixture Mod %04d\"\nfilename = \"%s\"\nside = \"%s\"\nversion = \"1.2.%d\"\n\n[download]\nurl = \"https://cdn.modrinth.dev/%s\"\nhash-format = \"sha256\"\nhash = \"%s\"\n\n[update.modrinth]\nmod-id = \"MOD%04dX\"\nversion-id = \"v1.2.%d\"\n",
			i, jarName, side, i%9+1, jarName, jarDigest, i, i%9+1)
	case 1: // curseforge
		content = fmt.Sprintf("name = \"Fixture Mod %04d\"\nfilename = \"%s\"\nside = \"%s\"\n\n[download]\nurl = \"https://media.curseforge.dev/%s\"\nhash-format = \"sha256\"\nhash = \"%s\"\n\n[update.curseforge]\nproject-id = %d\nfile-id = %d\n",
			i, jarName, side, jarName, jarDigest, 900000+i, 1000000+i)
	default: // url（无 provider 身份，扫描按路径回退）
		content = fmt.Sprintf("name = \"Fixture Mod %04d\"\nfilename = \"%s\"\nside = \"%s\"\n\n[download]\nurl = \"https://example.dev/%s\"\nhash-format = \"sha256\"\nhash = \"%s\"\n",
			i, jarName, side, jarName, jarDigest)
	}
	metaName = metaFileName(i)
	metaDigest, err = writeFile(ctx, filepath.Join(projectRoot, "mods", metaName), content)
	return metaName, metaDigest, err
}

// writeProjectPlainMod 写入第 i 个「无 CF 声明」mod 的 metafile（票 #60 变体）：
// 只有 name/filename/side，无 [download]/[update] 段——扫描面有 metafile 身份、
// 无任何重取信息（「重取性看数据不看出身」的 user_object_required 行来源）。
// 返回 metafile 文件名、内容摘要与运行端 JAR 文件名。
func writeProjectPlainMod(ctx context.Context, projectRoot string, opts Options, i int) (metaName, metaDigest, jarName string, err error) {
	jarName = fmt.Sprintf("plain-mod-%04d-1.0.jar", i)
	content := fmt.Sprintf("name = \"Plain Mod %04d\"\nfilename = \"%s\"\nside = \"both\"\n", i, jarName)
	metaName = fmt.Sprintf("fixture-plain-%04d.pw.toml", i)
	metaDigest, err = writeFile(ctx, filepath.Join(projectRoot, "mods", metaName), content)
	return metaName, metaDigest, jarName, err
}

// textRelPath 返回第 i 个受管文件的 root 相对路径：轮换 config/kubejs/scripts
// 前缀并带子目录（真实布局，WalkDir 多层）。
func textRelPath(i int) string {
	switch i % 5 {
	case 0:
		return fmt.Sprintf("config/feature-%04d.toml", i)
	case 1:
		return fmt.Sprintf("config/modules/mod-%04d.cfg", i)
	case 2:
		return fmt.Sprintf("kubejs/server_scripts/script-%04d.js", i)
	case 3:
		return fmt.Sprintf("kubejs/client_scripts/widget-%04d.js", i)
	default:
		return fmt.Sprintf("scripts/quest-%04d.js", i)
	}
}

// textSize 返回第 i 个受管文件的大小（1KB~100KB）。
func textSize(i int) int64 {
	rng := prng{state: fileSeed(0x51E7, uint64(i))}
	return 1<<10 + int64(rng.next()%uint64(100<<10-1<<10))
}

// writeRandomFile 以独立 PRNG 流写入 size 字节内容；seed%10==0 为伪随机
// 二进制（小型二进制样本），其余为行式伪文本。
func writeRandomFile(ctx context.Context, path string, seed uint64, size int64) (string, error) {
	rng := prng{state: seed}
	binary := seed%10 == 0
	return writeFileFunc(ctx, path, func(w io.Writer) error {
		buf := make([]byte, 64<<10)
		remaining := size
		for remaining > 0 {
			n := int64(len(buf))
			if remaining < n {
				n = remaining
			}
			chunk := buf[:n]
			rng.fill(chunk)
			if !binary {
				makeText(chunk, &rng)
			}
			if _, err := w.Write(chunk); err != nil {
				return err
			}
			remaining -= n
		}
		return nil
	})
}

// makeText 把缓冲区改写为行式可打印伪文本（词间空格、行尾换行）。
func makeText(b []byte, rng *prng) {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	word := 0
	for i := 0; i < len(b); i++ {
		switch {
		case word == 0:
			b[i] = letters[rng.next()%uint64(len(letters))]
			word = int(rng.next()%8) + 2
		case word == 1:
			b[i] = ' '
			word = 0
		default:
			b[i] = letters[rng.next()%uint64(len(letters))]
			word--
		}
		if i%72 == 71 {
			b[i] = '\n'
			word = 0
		}
	}
}

// writeFile 写入字符串内容并返回 sha256 hex。
func writeFile(ctx context.Context, path, content string) (string, error) {
	return writeFileFunc(ctx, path, func(w io.Writer) error {
		_, err := io.WriteString(w, content)
		return err
	})
}

// writeFileFunc 确保父目录存在、写文件并返回内容 sha256 hex。
func writeFileFunc(ctx context.Context, path string, produce func(io.Writer) error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("perffixture: 创建目录 %s: %w", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("perffixture: 创建 %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if err := produce(io.MultiWriter(f, h)); err != nil {
		return "", fmt.Errorf("perffixture: 写入 %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileDigest 读取既有文件计算 sha256 hex（pack.toml 内嵌 index.toml 摘要用）。
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("perffixture: 打开 %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("perffixture: 摘要 %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
