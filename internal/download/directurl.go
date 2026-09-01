package download

import (
	"fmt"
	"log"
)

// cfFileBase 是 CurseForge CDN 免钥匙直链的固定前缀。研究笔记实测（2026-09-01）：
// mediafilez host 免 key（206/HEAD/Range 均 OK）；edge host 要 key，不可用。
const cfFileBase = "https://mediafilez.forgecdn.net/files"

// fileIDLegacyLimit：fileID 达 10^7（8 位）后分段口径两说分叉（`12345/678` vs
// `1234/5678`），当前不存在可实测对象。越界记日志、不换口径（ADR-0008 §2）：
// 错 URL 只表现为 404→降级，hash 兜底保证错内容装不进去；届现实测后改本函数即可。
const fileIDLegacyLimit = 10_000_000

// URLLog 是直链构造的日志出口（fileID ≥ 10^7 越界告警）；测试可替换捕获。
var URLLog = log.Printf

// DirectURL 构造 CurseForge 免钥匙下载直链（ADR-0008 §2）：
//
//	https://mediafilez.forgecdn.net/files/{file-id / 1000}/{file-id % 1000}/{filename}
//
// 两分段按整数格式化、不补零——实测钉死：8778011 → `8778/11` 得 206，
// `8778/011` 补零得 403。输入只有 packwiz metafile 的 filename 与
// update.curseforge.file-id（project-id 不需要）。
func DirectURL(fileID int64, filename string) string {
	return directURL(cfFileBase, fileID, filename)
}

// directURL 是 DirectURL 的 base 可注入版：假 CDN 单测用同一构造逻辑喂注入前缀，
// 保证生产口径与测试口径不漂移。
func directURL(base string, fileID int64, filename string) string {
	if fileID >= fileIDLegacyLimit {
		URLLog("download: fileID %d 达 8 位（≥10^7），CDN 分段口径未实测，按整数除法不换口径构造（%d/%d），届时实测再修",
			fileID, fileID/1000, fileID%1000)
	}
	// 整数除法、不补零：%d 直接格式化，无前导零（补零实测 403）
	return fmt.Sprintf("%s/%d/%d/%s", base, fileID/1000, fileID%1000, filename)
}
