package server

// Staff roles (#106). provider: their own patients, schedule and messages.
// office (front desk): the whole practice's schedule and requests, patients,
// pairing, orders, the store and the website, but no message threads and no
// staff management. admin: everything office has, plus setup.

// isStaffRole reports whether role is a practice staff role.
func isStaffRole(role string) bool {
	return role == "provider" || role == "office" || role == "admin"
}

// practiceWide reports whether role sees the whole practice's schedule,
// requests and patients rather than only its own.
func practiceWide(role string) bool {
	return role == "office" || role == "admin"
}
