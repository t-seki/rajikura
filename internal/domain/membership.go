package domain

// MaxAvailableDays は、会員種別で録音できる最大の日数（対象外でない放送回の日数）を返す。
// 規則は AvailableDays が持つ。放送回の情報を持たない側（番組表のキャッシュに持つ放送日の範囲など）が使う。
func MaxAvailableDays(m Membership) int {
	return AvailableDays(m, Airing{})
}

// AreaFree は、会員種別がエリアフリーを含む（エリアフリー・ダブルプラン）か。radiko のログインの応答の areafree と比べる。
func (m Membership) AreaFree() bool {
	return m == MembershipAreaFree || m == MembershipDouble
}

// HasTimeFree30 は、会員種別がタイムフリー30 を含む（タイムフリー30・ダブルプラン）か。
func (m Membership) HasTimeFree30() bool {
	return m == MembershipTimeFree30 || m == MembershipDouble
}

// Member は、会員種別が非会員でない（radiko のログインが要る）か。
func (m Membership) Member() bool {
	return m != MembershipFree
}
