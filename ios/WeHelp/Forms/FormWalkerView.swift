import SwiftUI

/// Fills in one form, one page per screen (#198). Answers live in the
/// FormSession, in memory only.
struct FormWalkerView: View {
    @State var session: FormSession
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        Group {
            if session.submitted { done } else { pageView }
        }
        .navigationTitle(session.definition.title)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var pageView: some View {
        VStack(spacing: 0) {
            ProgressView(value: Double(session.showsSubmit ? session.position : session.position - 1),
                         total: Double(max(session.pages.count, 1)))
                .accessibilityLabel("Page \(session.position) of \(session.pages.count)")
                .padding(.horizontal)
                .padding(.top, 8)
            Form {
                if let t = session.page.title ?? session.sectionTitle {
                    Section { Text(t).font(.title3.weight(.semibold)) }
                }
                ForEach(session.visibleItems, id: \.id) { item in
                    Section {
                        FormItemView(item: item, session: session)
                    } footer: {
                        if let p = session.problems[item.id] {
                            Label(p, systemImage: "exclamationmark.circle").foregroundStyle(.red)
                        }
                    }
                }
                if let e = session.saveError {
                    Section { Label(e, systemImage: "wifi.exclamationmark").foregroundStyle(.red) }
                }
            }
            // The number pad has no Return key: scrolling or Done dismisses it,
            // so the keyboard never hides Back and Next.
            .scrollDismissesKeyboard(.interactively)
            .toolbar {
                ToolbarItemGroup(placement: .keyboard) {
                    Spacer()
                    Button("Done") {
                        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
                    }
                }
            }
            controls
        }
    }

    private var controls: some View {
        HStack {
            Button("Back") { session.back() }
                .disabled(session.isFirst || session.saving)
                .frame(minWidth: 88, minHeight: 44)
            Spacer()
            if session.saving { ProgressView() }
            Spacer()
            if session.showsSubmit {
                Button("Submit") { Task { await session.submit() } }
                    .buttonStyle(.borderedProminent)
                    .frame(minWidth: 88, minHeight: 44)
                    .disabled(session.saving || session.closed)
            } else {
                // On a provisional last page Next still checks the page first.
                Button("Next") { Task { _ = await session.next() } }
                    .buttonStyle(.borderedProminent)
                    .frame(minWidth: 88, minHeight: 44)
                    .disabled(session.saving || session.closed)
            }
        }
        .padding()
        .background(.bar)
    }

    private var done: some View {
        List {
            Section {
                Label("Thank you", systemImage: "checkmark.seal").font(.title2.weight(.semibold))
                Text("Your answers have been sent to your practice.")
            }
            let shown = session.shownScores
            if !shown.isEmpty {
                Section("Your results") {
                    ForEach(shown, id: \.0.id) { sc, out in
                        LabeledContent(sc.text ?? sc.id) {
                            VStack(alignment: .trailing) {
                                Text(out.value ?? "—")
                                if let b = out.band, let text = sc.bands.first(where: { $0.id == b })?.text { Text(text).font(.caption) }
                            }
                        }
                    }
                }
            }
            Section { Button("Done") { dismiss() }.frame(minHeight: 44) }
        }
    }
}

/// A display item's picture (#198), loaded once through the pinned
/// connection and kept in memory. VoiceOver reads its description; if it
/// can't load, the description is shown instead. Video isn't played in the
/// app yet, so a video shows its description.
struct FormMediaView: View {
    let media: FormDefinition.Item.Media
    let session: FormSession

    var body: some View {
        Group {
            if media.kind == "image", let image = session.images[media.asset] {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFit()
                    .frame(maxWidth: .infinity, maxHeight: 360)
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    .accessibilityLabel(media.alt.isEmpty ? "Picture" : media.alt)
            } else if media.kind == "image", !session.failedImages.contains(media.asset) {
                ProgressView()
                    .frame(maxWidth: .infinity, minHeight: 120)
                    .accessibilityLabel("Loading picture")
            } else if !media.alt.isEmpty {
                Label(media.alt, systemImage: media.kind == "video" ? "film" : "photo")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
        }
        .task(id: media.asset) {
            if media.kind == "image" { await session.loadImage(media.asset) }
        }
    }
}

/// One question, with the control its type and `control` hint ask for.
struct FormItemView: View {
    let item: FormDefinition.Item
    let session: FormSession

    private var value: FormJSON? { session.answer(item.id) }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if !item.text.isEmpty {
                Text(item.text + (item.required ? " *" : ""))
                    .font(.body.weight(item.type == "display" ? .regular : .medium))
                    .accessibilityLabel(item.text + (item.required ? ", required" : ""))
            }
            control
        }
        .padding(.vertical, 4)
    }

    @ViewBuilder private var control: some View {
        switch item.type {
        case "choice": choice
        case "boolean": yesNo
        case "integer", "decimal": number
        case "date": date
        case "text": text
        default:
            // display: the text above is the item, plus its picture if any.
            if let media = item.media { FormMediaView(media: media, session: session) }
        }
    }

    // MARK: choice

    @ViewBuilder private var choice: some View {
        if item.multiple {
            let chosen = Set(value?.array?.compactMap(\.string) ?? [])
            ForEach(item.options, id: \.value) { o in
                Button {
                    var next = item.options.map(\.value).filter { chosen.contains($0) }
                    if chosen.contains(o.value) { next.removeAll { $0 == o.value } } else {
                        next = item.options.map(\.value).filter { chosen.contains($0) || $0 == o.value }
                    }
                    session.set(item.id, next.isEmpty ? nil : .array(next.map { .string($0) }))
                } label: {
                    Label(o.text, systemImage: chosen.contains(o.value) ? "checkmark.square.fill" : "square")
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(chosen.contains(o.value) ? .isSelected : [])
            }
        } else if item.control == "dropdown" {
            Picker(item.text, selection: Binding(
                get: { value?.string ?? "" },
                set: { session.set(item.id, $0.isEmpty ? nil : .string($0)) })) {
                Text("Choose…").tag("")
                ForEach(item.options, id: \.value) { Text($0.text).tag($0.value) }
            }
            .labelsHidden()
            .frame(minHeight: 44)
        } else if item.control == "buttons" {
            HStack {
                ForEach(item.options, id: \.value) { o in optionButton(o.text, selected: value?.string == o.value) {
                    session.set(item.id, .string(o.value))
                } }
            }
        } else {
            ForEach(item.options, id: \.value) { o in
                Button { session.set(item.id, .string(o.value)) } label: {
                    Label(o.text, systemImage: value?.string == o.value ? "largecircle.fill.circle" : "circle")
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(value?.string == o.value ? .isSelected : [])
            }
        }
    }

    private func optionButton(_ title: String, selected: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).frame(maxWidth: .infinity, minHeight: 44)
        }
        .buttonStyle(.bordered)
        .tint(selected ? .accentColor : .secondary)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    // MARK: yes/no

    private var yesNo: some View {
        HStack {
            optionButton("Yes", selected: value?.bool == true) { session.set(item.id, .bool(true)) }
            optionButton("No", selected: value?.bool == false) { session.set(item.id, .bool(false)) }
        }
    }

    // MARK: numbers

    @ViewBuilder private var number: some View {
        if item.control == "slider", let mn = item.min, let mx = item.max,
           let lo = Double(mn.text), let hi = Double(mx.text), hi > lo {
            let step = item.step.flatMap { Double($0.text) } ?? 1
            let current = value.flatMap { if case let .number(t) = $0 { Double(t) } else { nil } }
            VStack(alignment: .leading) {
                Slider(value: Binding(
                    get: { current ?? lo },
                    set: { session.set(item.id, .number(Self.numberText($0, integer: item.type == "integer", step: step))) }),
                       in: lo...hi, step: step) {
                    Text(item.text)
                } minimumValueLabel: { Text(item.minLabel ?? mn.text).font(.caption) }
                  maximumValueLabel: { Text(item.maxLabel ?? mx.text).font(.caption) }
                Text(current.map { Self.numberText($0, integer: item.type == "integer", step: step) } ?? "Not answered")
                    .font(.caption).foregroundStyle(.secondary)
            }
        } else {
            HStack {
                TextField(item.type == "integer" ? "Whole number" : "Number", text: Binding(
                    get: { if case let .number(t)? = value { t } else { "" } },
                    set: { t in
                        let trimmed = t.trimmingCharacters(in: .whitespaces)
                        session.set(item.id, trimmed.isEmpty ? nil : .number(trimmed))
                    }))
                    .keyboardType(item.type == "integer" ? .numberPad : .decimalPad)
                    .frame(minHeight: 44)
                    .accessibilityLabel(item.text)
                if let u = item.unit { Text(Self.unitText(u)).foregroundStyle(.secondary) }
            }
        }
    }

    /// Slider values as a decimal literal (never through scientific notation).
    static func numberText(_ v: Double, integer: Bool, step: Double) -> String {
        if integer { return String(Int(v.rounded())) }
        let places = max(0, min(6, Int((-log10(step)).rounded(.up))))
        var s = String(format: "%.\(places)f", v)
        if s.contains(".") { while s.hasSuffix("0") { s.removeLast() }; if s.hasSuffix(".") { s.removeLast() } }
        return s == "-0" ? "0" : s
    }

    static func unitText(_ ucum: String) -> String {
        ["Cel": "°C", "[degF]": "°F", "kg": "kg", "[lb_av]": "lb", "cm": "cm", "mm[Hg]": "mmHg", "/min": "per minute", "a": "years"][ucum] ?? ucum
    }

    // MARK: date

    private static let dayFormat: DateFormatter = {
        let f = DateFormatter()
        f.calendar = Calendar(identifier: .gregorian)
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()

    private func day(_ s: String) -> Date? {
        let t = s == "today" ? FormDates.today() : s
        return t.isEmpty ? nil : Self.dayFormat.date(from: t)
    }

    @ViewBuilder private var date: some View {
        let lo = day(item.minDate) ?? .distantPast
        let hi = day(item.maxDate) ?? .distantFuture
        if let s = value?.string, let d = Self.dayFormat.date(from: s) {
            HStack {
                DatePicker(item.text, selection: Binding(
                    get: { d }, set: { session.set(item.id, .string(Self.dayFormat.string(from: $0))) }),
                           in: lo...hi, displayedComponents: .date)
                    .labelsHidden()
                Button("Clear") { session.set(item.id, nil) }.frame(minHeight: 44)
            }
        } else {
            Button("Choose a date") {
                let start = min(max(Date(), lo), hi)
                session.set(item.id, .string(Self.dayFormat.string(from: start)))
            }
            .frame(minHeight: 44)
        }
    }

    // MARK: text

    @ViewBuilder private var text: some View {
        let s = value?.string ?? ""
        let binding = Binding(get: { s }, set: { session.set(item.id, $0.isEmpty ? nil : .string($0)) })
        VStack(alignment: .trailing, spacing: 4) {
            if item.control == "long" {
                TextField("Your answer", text: binding, axis: .vertical)
                    .lineLimit(4...12)
                    .accessibilityLabel(item.text)
            } else {
                TextField("Your answer", text: binding)
                    .frame(minHeight: 44)
                    .accessibilityLabel(item.text)
            }
            Text("\(s.unicodeScalars.count)/\(item.maxLength)")
                .font(.caption2)
                .foregroundStyle(s.unicodeScalars.count > item.maxLength ? .red : .secondary)
        }
    }
}
