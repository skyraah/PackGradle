// 合并预览的渲染层计算（票 #94，契约 07 §3.4）：后端只供 content（合并后全文）
// 与 base_content（基线全文）两段——行级绿（新增）/红（删除）/黄（修改）标注与
// 语法高亮都是本文件的渲染层职责。
//   - 行级粒度（不做字符级，与冲突块证据同粒度）：对 base vs merged 做行级
//     LCS 对齐；相邻的删除/新增游程按行 1:1 配对折叠为「修改」（黄），
//     余量保持纯删除（红）/纯新增（绿）。
//   - 语法高亮按扩展名分派（toml/json/js/java），未识别类型退纯文本。
// 全部为纯函数，无副作用；逐行分词产出 span 片段，经 Vue 插值渲染（不走
// v-html，无注入面）。

// ---- 行级 diff ----

export type DiffRowType = 'same' | 'added' | 'removed' | 'modified'

export interface DiffRow {
    type: DiffRowType
    /** 行文本（modified/added 取合并侧；removed 取基线侧） */
    text: string
    /** modified 行的被替换基线行文本（title 提示用），其余为 undefined */
    replaced?: string
}

const DIFF_GUARD = 2500 // LCS 方阵边长上限（超出退化为整段增删，防 O(n·m) 爆内存）

/** 行切分：保留空行语义；末行无换行符时仍是合法行。 */
function splitLines(text: string): string[] {
    if (text === '') return []
    const lines = text.split('\n')
    // 结尾换行符产生的尾空元素不是一行，丢弃（与后端 splitLines 口径一致）
    if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
    return lines
}

/**
 * 对基线与合并后全文做行级标注（绿=新增/红=删除/黄=修改，其余原样）。
 * 行序即阅读序：删除行内联出现在其原位置，新增行内联出现在插入位置。
 */
export function diffRows(baseContent: string, content: string): DiffRow[] {
    const base = splitLines(baseContent)
    const merged = splitLines(content)

    // 首尾公共前/后缀裁剪：合并通常是局部改动，先把无差异区摘掉再跑 LCS。
    let pre = 0
    while (pre < base.length && pre < merged.length && base[pre] === merged[pre]) pre++
    let suf = 0
    while (
        suf < base.length - pre &&
        suf < merged.length - pre &&
        base[base.length - 1 - suf] === merged[merged.length - 1 - suf]
    )
        suf++
    const baseMid = base.slice(pre, base.length - suf)
    const mergedMid = merged.slice(pre, merged.length - suf)

    const rows: DiffRow[] = []
    for (const text of base.slice(0, pre)) rows.push({ type: 'same', text })

    if (baseMid.length === 0 && mergedMid.length === 0) {
        // 中段为空：纯前后缀即全部
    } else if (baseMid.length === 0) {
        for (const text of mergedMid) rows.push({ type: 'added', text })
    } else if (mergedMid.length === 0) {
        for (const text of baseMid) rows.push({ type: 'removed', text })
    } else if (baseMid.length > DIFF_GUARD || mergedMid.length > DIFF_GUARD) {
        // 超出护栏：不做对齐，整段按「删除+新增」呈现（保守不失真）
        for (const text of baseMid) rows.push({ type: 'removed', text })
        for (const text of mergedMid) rows.push({ type: 'added', text })
    } else {
        alignMid(baseMid, mergedMid, rows)
    }

    for (const text of base.slice(base.length - suf)) rows.push({ type: 'same', text })
    return rows
}

/** alignMid 对中段做 LCS 对齐并折叠「修改」配对，行序追加进 rows。 */
function alignMid(base: string[], merged: string[], rows: DiffRow[]): void {
    const n = base.length
    const m = merged.length
    // dp[i][j] = base[i:] 与 merged[j:] 的 LCS 长度
    const dp: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
    for (let i = n - 1; i >= 0; i--) {
        for (let j = m - 1; j >= 0; j--) {
            dp[i][j] = base[i] === merged[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
        }
    }
    // 回溯出 base-only / merged-only / same 游程（same 行直接落行）
    type Run = { kind: 'base' | 'merged'; text: string }
    const runs: Run[] = []
    let i = 0
    let j = 0
    while (i < n && j < m) {
        if (base[i] === merged[j]) {
            flushPairs(runs, rows)
            runs.length = 0
            rows.push({ type: 'same', text: base[i] })
            i++
            j++
        } else if (dp[i + 1][j] >= dp[i][j + 1]) {
            runs.push({ kind: 'base', text: base[i++] })
        } else {
            runs.push({ kind: 'merged', text: merged[j++] })
        }
    }
    while (i < n) runs.push({ kind: 'base', text: base[i++] })
    while (j < m) runs.push({ kind: 'merged', text: merged[j++] })
    flushPairs(runs, rows)
}

/** flushPairs 把同块内的删除/新增行按序 1:1 配对为「修改」（黄），余量落红/绿。 */
function flushPairs(runs: { kind: 'base' | 'merged'; text: string }[], rows: DiffRow[]): void {
    const baseRun = runs.filter(r => r.kind === 'base').map(r => r.text)
    const mergedRun = runs.filter(r => r.kind === 'merged').map(r => r.text)
    const paired = Math.min(baseRun.length, mergedRun.length)
    for (let k = 0; k < paired; k++) {
        rows.push({ type: 'modified', text: mergedRun[k], replaced: baseRun[k] })
    }
    for (let k = paired; k < baseRun.length; k++) rows.push({ type: 'removed', text: baseRun[k] })
    for (let k = paired; k < mergedRun.length; k++) rows.push({ type: 'added', text: mergedRun[k] })
}

// ---- 语法高亮（扩展名分派，未识别退纯文本）----

export type HighlightLang = 'toml' | 'json' | 'js' | 'java' | 'plain'

export interface TokenSeg {
    text: string
    cls: string // 空 = 默认前景色
}

/** 按资源相对路径的扩展名分派高亮语言（契约 07 §3.4）。 */
export function langOfPath(relativePath: string): HighlightLang {
    const lower = (relativePath.split('/').pop() ?? '').toLowerCase()
    if (lower.endsWith('.toml')) return 'toml'
    if (lower.endsWith('.json')) return 'json'
    if (lower.endsWith('.js') || lower.endsWith('.mjs') || lower.endsWith('.cjs')) return 'js'
    if (lower.endsWith('.java')) return 'java'
    return 'plain'
}

const JS_KEYWORDS = new Set([
    'const', 'let', 'var', 'function', 'return', 'if', 'else', 'for', 'while', 'do', 'switch',
    'case', 'break', 'continue', 'class', 'extends', 'super', 'new', 'import', 'export', 'from',
    'default', 'async', 'await', 'try', 'catch', 'finally', 'throw', 'typeof', 'instanceof',
    'in', 'of', 'this', 'null', 'undefined', 'true', 'false', 'void', 'delete', 'yield',
    'static', 'get', 'set',
])

const JAVA_KEYWORDS = new Set([
    'public', 'private', 'protected', 'class', 'interface', 'enum', 'extends', 'implements',
    'static', 'final', 'void', 'int', 'long', 'double', 'float', 'boolean', 'char', 'byte',
    'short', 'return', 'if', 'else', 'for', 'while', 'do', 'switch', 'case', 'break',
    'continue', 'new', 'import', 'package', 'this', 'super', 'null', 'true', 'false',
    'try', 'catch', 'finally', 'throw', 'throws', 'abstract', 'synchronized', 'volatile',
    'transient', 'instanceof', 'default', 'assert', 'var', 'record', 'sealed', 'yield',
])

// 单行分词正则：交替分支顺序即优先级（注释/字符串优先于关键字）。
const TOKEN_PATTERNS: Partial<Record<HighlightLang, RegExp>> = {
    toml:
        /(?<com>#.*)|(?<sec>\[[^\]]*\])|(?<str>"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|(?<key>^[ \t]*[\w.$-]+(?=[ \t]*=))|(?<num>\b(?:true|false|\d[\d_.]*(?:[eE][+-]?\d+)?)\b)/g,
    json:
        /(?<key>"(?:[^"\\]|\\.)*"[ \t]*(?=:))|(?<str>"(?:[^"\\]|\\.)*")|(?<num>-?\b\d[\d.eE+-]*\b)|(?<kw>\b(?:true|false|null)\b)/g,
    js:
        /(?<com>\/\/.*|\/\*.*?(?:\*\/|$))|(?<str>"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`)|(?<num>\b\d[\w.]*\b)|(?<kw>[A-Za-z_$][\w$]*)/g,
    java:
        /(?<com>\/\/.*|\/\*.*?(?:\*\/|$))|(?<str>"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|(?<ann>@\w+)|(?<num>\b\d[\w.]*\b)|(?<kw>[A-Za-z_$][\w$]*)/g,
}

const CLS = {
    com: 'text-muted-foreground italic',
    str: 'text-emerald-600 dark:text-emerald-400',
    key: 'text-sky-600 dark:text-sky-400',
    sec: 'text-fuchsia-600 dark:text-fuchsia-400 font-semibold',
    num: 'text-amber-600 dark:text-amber-400',
    kw: 'text-violet-600 dark:text-violet-400',
    ann: 'text-amber-600 dark:text-amber-400',
} as const

/** 对单行做语法分词；plain 语言或无命中时返回单一默认片段。 */
export function highlightLine(line: string, lang: HighlightLang): TokenSeg[] {
    const re = TOKEN_PATTERNS[lang]
    if (!re) return [{ text: line, cls: '' }]
    const out: TokenSeg[] = []
    let last = 0
    re.lastIndex = 0
    for (let m = re.exec(line); m !== null; m = re.exec(line)) {
        if (m.index > last) out.push({ text: line.slice(last, m.index), cls: '' })
        const groups = m.groups ?? {}
        let cls = ''
        for (const name of Object.keys(groups)) {
            if (groups[name] !== undefined) {
                cls = name === 'kw' ? keywordCls(lang, groups[name]) : CLS[name as keyof typeof CLS] ?? ''
                break
            }
        }
        out.push({ text: m[0], cls })
        last = m.index + m[0].length
        if (m[0].length === 0) re.lastIndex++ // 零宽匹配防线
    }
    if (last < line.length) out.push({ text: line.slice(last), cls: '' })
    return out
}

function keywordCls(lang: HighlightLang, word: string): string {
    if (lang === 'js' && JS_KEYWORDS.has(word)) return CLS.kw
    if (lang === 'java' && JAVA_KEYWORDS.has(word)) return CLS.kw
    return ''
}
