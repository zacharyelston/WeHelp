import Foundation

/// The patient's form routes (#196, #197; docs/MOBILE_CLIENT.md). Bodies are
/// read and written as FormJSON so numeric answers keep their exact text.
enum FormsAPIError: Error, LocalizedError, Equatable {
    /// The forms switch is off at this practice.
    case featureOff
    /// The occurrence isn't open yet.
    case notOpen
    /// The window has closed; nothing more can be saved or submitted.
    case windowClosed
    case alreadySubmitted
    /// Another save got there first; refetch the response and save again.
    case staleRevision(Int?)
    /// Answers the Back Office refused, per item (page_id on submit).
    case invalidAnswers([Problem])
    case notFound
    case server(Int, String)

    struct Problem: Equatable, Sendable {
        let item: String
        let pageID: String?
        let code: String
        let message: String
    }

    var errorDescription: String? {
        switch self {
        case .featureOff: "Forms are turned off at your practice."
        case .notOpen: "This form isn't open yet."
        case .windowClosed: "This form has closed."
        case .alreadySubmitted: "You've already sent this form."
        case .staleRevision: "Your answers changed somewhere else. Please try again."
        case .invalidAnswers: "Some answers need another look."
        case .notFound: "This form isn't available."
        case let .server(_, msg): msg
        }
    }

    static func from(status: Int, data: Data) -> FormsAPIError {
        let body = try? FormJSON.parse(data)
        let code = body?["code"]?.string
        let message = body?["error"]?.string ?? "Server error \(status)"
        switch (status, code) {
        case (403, "feature_off"): return .featureOff
        case (404, _): return .notFound
        case (409, "not_open"): return .notOpen
        case (409, "window_closed"): return .windowClosed
        case (409, "already_submitted"): return .alreadySubmitted
        case (409, "stale_revision"): return .staleRevision(body?["revision"]?.intValue)
        case (422, "invalid_answers"):
            let problems = (body?["errors"]?.array ?? []).map {
                Problem(item: $0["item"]?.string ?? "", pageID: $0["page_id"]?.string,
                        code: $0["code"]?.string ?? "", message: $0["message"]?.string ?? "")
            }
            return .invalidAnswers(problems)
        default: return .server(status, message)
        }
    }
}

/// One entry of GET /me/forms.
struct AssignedForm: Identifiable, Sendable, Equatable {
    let occurrenceID: String
    let versionID: String
    let title: String
    let state: String       // "open" or "upcoming"
    let opensAt: Date?
    let dueAt: Date?
    let closesAt: Date?
    let responseID: String?  // an in-progress response to resume
    var id: String { occurrenceID }
    var isOpen: Bool { state == "open" }
}

/// The resume state from POST /responses.
struct FormResponseState: Sendable {
    let id: String
    let revision: Int
    let answers: [String: FormJSON]
    /// Pages with a save, in document order (resume opens the first page
    /// without one, or the last saved one).
    var savedPages: [String] = []
}

struct FormsAPI: Sendable {
    let api: APIClient

    private func call(_ method: String, _ path: String, body: FormJSON? = nil, headers: [String: String] = [:]) async throws -> FormJSON {
        let (status, data) = try await api.authorizedRaw(method, path, rawBody: body.map { Data($0.text.utf8) }, headers: headers)
        guard (200..<300).contains(status) else { throw FormsAPIError.from(status: status, data: data) }
        return data.isEmpty ? .object([]) : try FormJSON.parse(data)
    }

    /// The practice's `forms` switch (GET /practice features).
    func formsEnabled() async throws -> Bool {
        let p = try await call("GET", "api/v1/practice")
        return p["features"]?["forms"]?.bool ?? false
    }

    func myForms() async throws -> [AssignedForm] {
        let r = try await call("GET", "api/v1/me/forms")
        return (r["forms"]?.array ?? []).compactMap { f in
            guard let occ = f["occurrence_id"]?.string, let ver = f["version_id"]?.string else { return nil }
            let date = { (k: String) in f[k]?.string.flatMap(APIClient.parseRFC3339) }
            return AssignedForm(occurrenceID: occ, versionID: ver, title: f["title"]?.string ?? "Form",
                                state: f["state"]?.string ?? "upcoming",
                                opensAt: date("opens_at"), dueAt: date("due_at"), closesAt: date("closes_at"),
                                responseID: f["response"]?["id"]?.string)
        }
    }

    /// The published version's definition (only for the patient's open occurrences).
    func definition(versionID: String) async throws -> FormDefinition {
        let v = try await call("GET", "api/v1/form-versions/\(versionID)")
        guard let def = v["definition"] else { throw FormsAPIError.server(200, "The form has no definition.") }
        return try FormDefinition(def)
    }

    /// An image on the version (PNG or JPEG bytes), only for the patient's
    /// open occurrences. The transport's session is ephemeral, so nothing is
    /// cached on disk; the walker keeps decoded images in memory.
    func asset(versionID: String, name: String) async throws -> Data {
        let path = "api/v1/form-versions/\(versionID)/assets/" + (name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? name)
        let (status, data) = try await api.authorizedRaw("GET", path)
        guard (200..<300).contains(status) else { throw FormsAPIError.from(status: status, data: data) }
        guard FormImages.isPNGOrJPEG(data) else { throw FormsAPIError.server(status, "Not an image.") }
        return data
    }

    /// Starts a response, or returns the in-progress one with its saved answers.
    func start(occurrenceID: String) async throws -> FormResponseState {
        let r = try await call("POST", "api/v1/responses", body: .object([("occurrence_id", .string(occurrenceID))]))
        guard let id = r["id"]?.string else { throw FormsAPIError.server(200, "No response id.") }
        return FormResponseState(id: id, revision: r["revision"]?.intValue ?? 0,
                                 answers: Dictionary(uniqueKeysWithValues: r["answers"]?.objectPairs ?? []),
                                 savedPages: (r["pages"]?.array ?? []).compactMap { $0["page_id"]?.string })
    }

    /// Saves one page; returns the new revision.
    func savePage(responseID: String, pageID: String, answers: [String: FormJSON], revision: Int, idempotencyKey: String) async throws -> Int {
        let body = FormJSON.object([("answers", .object(answers.sorted { $0.key < $1.key }))])
        let r = try await call("PUT", "api/v1/responses/\(responseID)/pages/\(pageID)", body: body,
                               headers: ["If-Match": "\"\(revision)\"", "Idempotency-Key": idempotencyKey])
        return r["revision"]?.intValue ?? revision + 1
    }

    /// Submits; returns the scores the form shows the patient (showScore).
    func submit(responseID: String, idempotencyKey: String) async throws -> [String: FormResult.ScoreOut] {
        let r = try await call("POST", "api/v1/responses/\(responseID)/submit", headers: ["Idempotency-Key": idempotencyKey])
        var out: [String: FormResult.ScoreOut] = [:]
        for (id, s) in r["scores"]?.objectPairs ?? [] {
            out[id] = FormResult.ScoreOut(value: s["value"]?.string, band: s["band"]?.string, reason: nil)
        }
        return out
    }
}

/// Page saves and submit through the Back Office (#197). One per open walker.
/// A retry of the same page with the same answers reuses its idempotency key,
/// so a save that reached the server but lost its reply isn't applied twice.
actor APIFormSink: FormSink {
    let forms: FormsAPI
    let occurrenceID: String
    private(set) var responseID: String
    private var revision: Int
    private var pending: (page: String, answers: [String: FormJSON], key: String)?
    private var submitKey = UUID().uuidString.lowercased()

    init(forms: FormsAPI, occurrenceID: String, state: FormResponseState) {
        self.forms = forms
        self.occurrenceID = occurrenceID
        responseID = state.id
        revision = state.revision
    }

    func savePage(_ pageID: String, answers: [String: FormJSON]) async throws {
        let key: String
        if let p = pending, p.page == pageID, p.answers == answers { key = p.key } else {
            key = UUID().uuidString.lowercased()
            pending = (pageID, answers, key)
        }
        revision = try await forms.savePage(responseID: responseID, pageID: pageID, answers: answers, revision: revision, idempotencyKey: key)
        pending = nil
    }

    /// After a stale revision (another device saved): fetch what the Back
    /// Office holds now. The walker shows those answers and merges nothing
    /// (docs/MOBILE_CLIENT.md §16).
    func reload() async throws -> FormResponseState? {
        let fresh = try await forms.start(occurrenceID: occurrenceID)
        responseID = fresh.id
        revision = fresh.revision
        pending = nil
        return fresh
    }

    func submit(answers: [String: FormJSON]) async throws -> [String: FormResult.ScoreOut]? {
        // The server rebuilds the answers from the saved pages; `answers` is unused.
        try await forms.submit(responseID: responseID, idempotencyKey: submitKey)
    }
}
