package rag

import "strings"

// This small, project-maintained list removes conversational phrasing only
// from lexical queries, before Chinese bigrams are generated. Longer phrases
// precede shorter ones. Spaces keep terms on either side from joining together.
var queryBoilerplate = strings.NewReplacer(
	"能不能帮我", " ",
	"可以帮我", " ",
	"帮我看一下", " ",
	"帮我看看", " ",
	"我想知道", " ",
	"我想了解", " ",
	"你知道", " ",
	"您知道", " ",
	"请问一下", " ",
	"请问", " ",
	"解释一下", " ",
	"介绍一下", " ",
	"说明一下", " ",
	"告诉我", " ",
	"帮我", " ",
	"看一下", " ",
	"可不可以", " ",
	"能不能", " ",
	"是什么", " ",
	"什么是", " ",
	"为什么", " ",
	"什么", " ",
	"如何", " ",
	"怎么", " ",
	"哪几种", " ",
	"哪些", " ",
	"哪种", " ",
	"一下", " ",
)

// Only standalone terms are removed here. Do not delete single characters
// inside Chinese words: e.g. removing 有 would damage 有序. Negations and domain
// terms such as 不、文件、方法、读取 are deliberately retained.
var chineseQueryStopWords = map[string]bool{
	"我": true, "你": true, "您": true, "他": true, "她": true, "它": true,
	"的": true, "了": true, "吗": true, "呢": true, "吧": true, "啊": true,
	"呀": true, "是": true, "和": true, "与": true, "及": true, "请": true,
}

func queryCodeTerms(query string) []string {
	terms := codeTerms(queryBoilerplate.Replace(query))
	filtered := terms[:0]
	for _, term := range terms {
		if !chineseQueryStopWords[term] {
			filtered = append(filtered, term)
		}
	}
	return filtered
}
