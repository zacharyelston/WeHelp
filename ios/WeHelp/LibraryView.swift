import QuickLook
import SwiftUI

/// The practice library (#97): podcast links open in the browser or podcast
/// app; downloads come through the pinned tunnel and open in Quick Look.
@MainActor @Observable
final class LibraryModel {
    var library: Library?
    var error: String?
    var opening: UUID?
    var preview: URL?

    func load(_ api: APIClient) async {
        do {
            library = try await api.library()
            error = nil
        } catch {
            self.error = StoreModel.message(error)
        }
    }

    func open(_ file: Library.LibraryFile, _ api: APIClient) async {
        opening = file.id
        defer { opening = nil }
        do {
            let data = try await api.libraryFile(file.id)
            // A per-file temp copy for Quick Look. Practice handouts, not
            // patient data; the system clears tmp.
            let dir = FileManager.default.temporaryDirectory.appending(path: "library", directoryHint: .isDirectory)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let url = dir.appending(path: file.filename)
            try data.write(to: url, options: .atomic)
            preview = url
        } catch {
            self.error = StoreModel.message(error)
        }
    }
}

struct LibraryView: View {
    @Environment(AppState.self) private var appState
    @State private var model = LibraryModel()

    var body: some View {
        List {
            if let error = model.error {
                Text(error).foregroundStyle(.red)
            }
            if let lib = model.library {
                if !lib.podcasts.isEmpty {
                    Section("Podcasts") {
                        ForEach(lib.podcasts, id: \.self) { p in
                            if let url = URL(string: p.url), url.scheme == "https" {
                                Link(destination: url) {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Label(p.title, systemImage: "headphones")
                                        if let d = p.description, !d.isEmpty {
                                            Text(d).font(.caption).foregroundStyle(.secondary)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                if !lib.files.isEmpty {
                    Section("Downloads") {
                        ForEach(lib.files) { f in
                            Button {
                                Task { await model.open(f, appState.api) }
                            } label: {
                                HStack {
                                    Label(f.title, systemImage: "doc.richtext")
                                    Spacer()
                                    if model.opening == f.id {
                                        ProgressView()
                                    } else {
                                        Text(ByteCountFormatter.string(fromByteCount: Int64(f.size), countStyle: .file))
                                            .font(.caption).foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                    }
                }
                if lib.podcasts.isEmpty && lib.files.isEmpty {
                    ContentUnavailableView("Nothing here yet", systemImage: "books.vertical",
                                           description: Text("Your practice hasn't added anything to the library."))
                }
                if let site = URL(string: lib.siteURL), site.scheme == "https" {
                    Section {
                        Link(destination: site) { Label("Practice website", systemImage: "globe") }
                    }
                }
            }
        }
        .navigationTitle("Library")
        .quickLookPreview($model.preview)
        .refreshable { await model.load(appState.api) }
        .task { await model.load(appState.api) }
    }
}
