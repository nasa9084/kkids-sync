package titles

import "testing"

func TestParseSeries(t *testing.T) {
	tests := []struct {
		title string
		name  string
		vol   int
		ok    bool
	}{
		{"パリピ孔明（２７） (コミックDAYSコミックス)", "パリピ孔明", 27, true},
		{"幼女戦記(35) (角川コミックス・エース)", "幼女戦記", 35, true},
		{"シャングリラ・フロンティア（１６） ～クソゲーハンター、神ゲーに挑まんとす～ (週刊少年マガジンコミックス)", "シャングリラ・フロンティア", 16, true},
		{"港町ミステリー 4", "港町ミステリー", 4, true},
		{"忍空～SECOND STAGE 干支忍編～ 6 (ジャンプコミックスDIGITAL)", "忍空~SECOND STAGE 干支忍編~", 6, true},
		{"ＮＩＮＫＵ─忍空─ 3 (ジャンプコミックスDIGITAL)", "NINKU─忍空─", 3, true},
		{"BLUE GIANT MOMENTUM（９） (ビッグコミックススペシャル)", "BLUE GIANT MOMENTUM", 9, true},
		{"葬送のフリーレン 第12巻", "葬送のフリーレン", 12, true},
		{"30まで我慢したら一緒に暮らそうって言ったよね? 【連載版】 10 (BC)", "30まで我慢したら一緒に暮らそうって言ったよね? 【連載版】", 10, true},
		{"アイシールド21 11 (ジャンプコミックスDIGITAL)", "アイシールド21", 11, true},
		{"30まで独りだったら一緒に暮らそうって言ったよね？ 【連載版】５ (BC)", "30まで独りだったら一緒に暮らそうって言ったよね? 【連載版】", 5, true},
		{"異世界転移、地雷付き。２ (ドラゴンノベルス)", "異世界転移、地雷付き。", 2, true},
		{"山の上の喫茶店", "", 0, false},
		{"NHK 趣味の園芸 2026年 10月号 [雑誌] (NHKテキスト)", "", 0, false},
		{"やさしい家計の本 (ほにゃらら文庫)", "", 0, false},
	}
	for _, tt := range tests {
		name, vol, ok := ParseSeries(tt.title)
		if name != tt.name || vol != tt.vol || ok != tt.ok {
			t.Errorf("ParseSeries(%q) = (%q, %d, %v), want (%q, %d, %v)", tt.title, name, vol, ok, tt.name, tt.vol, tt.ok)
		}
	}
}

func TestDisplayTitle(t *testing.T) {
	tests := map[string]string{
		"鬼灯さん家のアネキ(2) (角川コミックス・エース・エクストラ)":       "鬼灯さん家のアネキ(2)",
		"パリピ孔明（２７） (コミックDAYSコミックス)":              "パリピ孔明（２７）",
		"30まで我慢したら一緒に暮らそうって言ったよね? 【連載版】 10 (BC)": "30まで我慢したら一緒に暮らそうって言ったよね? 【連載版】 10",
		"幼女戦記(35)": "幼女戦記(35)",
		"山の上の喫茶店":  "山の上の喫茶店",
		"NHK 趣味の園芸 2026年 10月号 [雑誌] (NHKテキスト)": "NHK 趣味の園芸 2026年 10月号",
	}
	for in, want := range tests {
		if got := DisplayTitle(in); got != want {
			t.Errorf("DisplayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
