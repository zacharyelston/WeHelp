import Foundation
import Observation

/// Where a walker's answers go. The Back Office implementation (page saves
/// and submit, #197) comes with the response routes; until then the sample
/// form uses `InMemoryFormSink`. Answers are PHI: a sink never writes them to
/// disk.
protocol FormSink: Sendable {
    /// Saves one page's answers (PUT .../pages/{pageId}). Throws to block Next.
    func savePage(_ pageID: String, answers: [String: FormJSON]) async throws
    /// Submits the canonical answers. Returns the server's scores when the
    /// form shows them; the walker never shows its own preview as the result.
    func submit(answers: [String: FormJSON]) async throws -> [String: FormResult.ScoreOut]?
}

/// The sample form's sink: nothing leaves memory, and the "server" scores are
/// the local evaluation (there is no server for the sample).
struct InMemoryFormSink: FormSink {
    let definition: FormDefinition
    func savePage(_ pageID: String, answers: [String: FormJSON]) async throws {}
    func submit(answers: [String: FormJSON]) async throws -> [String: FormResult.ScoreOut]? {
        definition.evaluate(answers).scores
    }
}

/// One patient filling one form, page by page. Everything stays in memory:
/// leaving the walker or switching accounts drops it.
@MainActor @Observable
final class FormSession {
    let definition: FormDefinition
    let sink: any FormSink
    private(set) var answers: [String: FormJSON]
    private(set) var pageID: String
    /// Inline problems on the current page, by item id.
    private(set) var problems: [String: String] = [:]
    private(set) var saving = false
    private(set) var saveError: String?
    private(set) var submitted = false
    private(set) var resultScores: [String: FormResult.ScoreOut]?

    init(definition: FormDefinition, sink: any FormSink, answers: [String: FormJSON] = [:]) {
        self.definition = definition
        self.sink = sink
        self.answers = answers
        let walk = Self.walk(definition, answers)
        // Resume at the first page with an unanswered required item, else the first.
        let firstOpen = walk.first { pid in
            let page = definition.pages[definition.pageIndex[pid]!]
            return page.items.contains { ii in
                let it = definition.items[ii]
                return it.required && answers[it.id] == nil
            }
        }
        pageID = firstOpen ?? walk.first ?? definition.pages[0].id
    }

    var result: FormResult { definition.evaluate(answers) }

    /// The visible pages along the current path, in order.
    var pages: [String] { Self.walk(definition, answers) }

    static func walk(_ def: FormDefinition, _ answers: [String: FormJSON]) -> [String] {
        let r = def.evaluate(answers)
        return r.path.flatMap { sid in
            def.sections[def.sectionIndex[sid]!].pages.map { def.pages[$0].id }.filter { r.visible[$0] == true }
        }
    }

    var page: FormDefinition.Page { definition.pages[definition.pageIndex[pageID]!] }
    var position: Int { (pages.firstIndex(of: pageID) ?? 0) + 1 }
    var isFirst: Bool { pages.first == pageID }
    var isLast: Bool { pages.last == pageID }

    /// Every required visible item on this page has an answer. Until then the
    /// walk's end is provisional (a branch may depend on these answers), so the
    /// walker offers Next rather than Submit.
    var pageComplete: Bool {
        let r = result
        return visibleItems.allSatisfy { !$0.required || r.answers[$0.id] != nil }
    }

    /// Submit is offered only on a complete last page.
    var showsSubmit: Bool { isLast && pageComplete }
    var sectionTitle: String? { definition.sections[page.section].title }

    /// The items on this page that are visible right now.
    var visibleItems: [FormDefinition.Item] {
        let r = result
        return page.items.map { definition.items[$0] }.filter { r.visible[$0.id] == true }
    }

    func answer(_ id: String) -> FormJSON? { answers[id] }

    func set(_ id: String, _ value: FormJSON?) {
        answers[id] = value
        problems[id] = nil
    }

    /// Problems for this page's visible items (types, ranges, required).
    private func check() -> [String: String] {
        let checked = definition.checkAnswers(answers)
        let ids = Set(visibleItems.map(\.id))
        var out: [String: String] = [:]
        for e in checked.errors where ids.contains(e.item) && out[e.item] == nil {
            out[e.item] = Self.message(e)
        }
        return out
    }

    static func message(_ e: FormAnswerError) -> String {
        switch e.code {
        case "required": "Please answer this question."
        case "out_of_range": "That's outside the allowed range (\(e.message))."
        case "too_long": "That's too long (\(e.message))."
        case "wrong_type": "That doesn't look right. Check the format."
        default: e.message
        }
    }

    /// Checks and saves this page, then moves on. Returns false when the page
    /// has problems or the save failed (Next stays blocked).
    func next() async -> Bool {
        problems = check()
        guard problems.isEmpty else { return false }
        guard await save() else { return false }
        let walk = pages
        if let i = walk.firstIndex(of: pageID), i + 1 < walk.count { pageID = walk[i + 1] }
        return true
    }

    func back() {
        problems = [:]
        let walk = pages
        if let i = walk.firstIndex(of: pageID), i > 0 { pageID = walk[i - 1] }
    }

    func submit() async {
        problems = check()
        guard problems.isEmpty, await save() else { return }
        saving = true
        defer { saving = false }
        do {
            let canonical = definition.checkAnswers(answers).answers
            resultScores = try await sink.submit(answers: canonical)
            submitted = true
            saveError = nil
        } catch {
            saveError = "Couldn't send your answers. Check your connection and try again."
        }
    }

    private func save() async -> Bool {
        saving = true
        defer { saving = false }
        let ids = Set(page.items.map { definition.items[$0].id })
        let pageAnswers = answers.filter { ids.contains($0.key) }
        do {
            try await sink.savePage(pageID, answers: pageAnswers)
            saveError = nil
            return true
        } catch {
            saveError = "Couldn't save this page. Check your connection and try again."
            return false
        }
    }

    /// Scores the patient may see after submitting (showScore), from the sink.
    var shownScores: [(FormDefinition.Score, FormResult.ScoreOut)] {
        guard let s = resultScores else { return [] }
        return definition.scores.filter(\.showScore).compactMap { sc in s[sc.id].map { (sc, $0) } }
    }
}
