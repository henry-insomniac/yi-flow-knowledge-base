package sourcepolicy

import "strings"

func ClassifyExternalSourceFamily(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}

	for family, markers := range map[string][]string{
		"moegirl": {
			"moegirl",
			"萌娘百科",
			"zh.moegirl.org.cn",
		},
		"anime": {
			"anime",
			"二次元",
			"acgn",
		},
		"game": {
			"game",
			"genshin",
			"原神",
		},
		"external_fan_wiki": {
			"fandom.com",
			"fanwiki",
			"fan-wiki",
		},
	} {
		for _, marker := range markers {
			if strings.Contains(value, marker) {
				return family, true
			}
		}
	}
	return "", false
}

func ClassifyInternalYiFlowSourceFamily(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}

	for _, marker := range []string{
		"yi-flow",
		"yi_flow",
		"yiflow",
	} {
		if strings.Contains(value, marker) {
			return "yi_flow", true
		}
	}
	return "", false
}
