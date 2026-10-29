package domain

// MaxAvailableDays は、会員種別で録音できる最大の日数（対象外でない放送回の日数）を返す。
// 規則は AvailableDays が持つ。放送回の情報を持たない側（番組表のキャッシュに持つ放送日の範囲など）が使う。
func MaxAvailableDays(m Membership) int {
	return AvailableDays(m, Airing{})
}
