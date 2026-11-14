import SwiftUI

/// The supply store's state (#97): the catalogue, what's in the basket, and
/// the patient's orders.
@MainActor @Observable
final class StoreModel {
    var products: [Product] = []
    var payment = "Pay at pickup or by invoice."
    var quantities: [UUID: Int] = [:]
    var orders: [SupplyOrder] = []
    var note = ""
    var loading = false
    var placing = false
    var error: String?

    var categories: [(name: String, products: [Product])] {
        var out: [(name: String, products: [Product])] = []
        for p in products {
            let name = p.category.isEmpty ? "Supplies" : p.category
            if out.last?.name == name { out[out.count - 1].products.append(p) } else { out.append((name, [p])) }
        }
        return out
    }

    var itemCount: Int { quantities.values.reduce(0, +) }

    var totalCents: Int {
        products.reduce(0) { $0 + $1.priceCents * (quantities[$1.id] ?? 0) }
    }

    func load(_ api: APIClient) async {
        loading = true
        defer { loading = false }
        do {
            let list = try await api.products()
            products = list.products
            payment = list.payment
            orders = try await api.orders()
            error = nil
        } catch {
            self.error = Self.message(error)
        }
    }

    func place(_ api: APIClient) async -> Bool {
        placing = true
        defer { placing = false }
        do {
            let order = try await api.placeOrder(quantities, note: note.trimmingCharacters(in: .whitespacesAndNewlines))
            orders.insert(order, at: 0)
            quantities = [:]
            note = ""
            error = nil
            return true
        } catch {
            self.error = Self.message(error)
            return false
        }
    }

    func cancel(_ order: SupplyOrder, _ api: APIClient) async {
        do {
            let updated = try await api.cancelOrder(order.id)
            if let i = orders.firstIndex(where: { $0.id == order.id }) { orders[i] = updated }
        } catch {
            self.error = Self.message(error)
        }
    }

    static func message(_ error: Error) -> String {
        if case APIError.http(_, let msg) = error { return msg }
        return "Couldn't reach your practice. Try again in a moment."
    }
}

/// Order supplements and herbal supplies from the practice (#97). Payment is
/// at pickup or by invoice.
struct StoreView: View {
    @Environment(AppState.self) private var appState
    @State private var store = StoreModel()
    @State private var checkingOut = false

    var body: some View {
        NavigationStack {
            List {
                if let error = store.error {
                    Text(error).foregroundStyle(.red)
                }
                if !store.orders.isEmpty {
                    Section {
                        NavigationLink {
                            OrdersView(store: store)
                        } label: {
                            Label("My orders", systemImage: "shippingbox")
                                .badge(store.orders.filter { $0.status == .placed || $0.status == .ready }.count)
                        }
                    }
                }
                ForEach(store.categories, id: \.name) { cat in
                    Section(cat.name) {
                        ForEach(cat.products) { p in
                            ProductRow(product: p, quantity: Binding(
                                get: { store.quantities[p.id] ?? 0 },
                                set: { store.quantities[p.id] = $0 == 0 ? nil : $0 }))
                        }
                    }
                }
                if store.products.isEmpty && !store.loading && store.error == nil {
                    ContentUnavailableView("No supplies yet", systemImage: "leaf",
                                           description: Text("Your practice hasn't added any products."))
                }
            }
            .navigationTitle("Store")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    NavigationLink { LibraryView() } label: { Label("Library", systemImage: "books.vertical") }
                }
            }
            .safeAreaInset(edge: .bottom) {
                if store.itemCount > 0 {
                    Button {
                        checkingOut = true
                    } label: {
                        Text("Review order · \(store.itemCount) item\(store.itemCount == 1 ? "" : "s") · \(store.totalCents.dollars)")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .padding()
                    .background(.bar)
                }
            }
            .sheet(isPresented: $checkingOut) { CheckoutView(store: store) }
            .refreshable { await store.load(appState.api) }
            .task { await store.load(appState.api) }
        }
    }
}

struct ProductRow: View {
    let product: Product
    @Binding var quantity: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(product.name).font(.headline)
                Spacer()
                Text(product.priceCents.dollars).font(.subheadline.weight(.semibold))
            }
            if !product.unit.isEmpty {
                Text(product.unit).font(.caption).foregroundStyle(.secondary)
            }
            if !product.description.isEmpty {
                Text(product.description).font(.subheadline).foregroundStyle(.secondary)
            }
            Stepper(value: $quantity, in: 0...20) {
                Text(quantity == 0 ? "Add" : "Quantity: \(quantity)")
                    .font(.subheadline)
            }
        }
        .padding(.vertical, 4)
    }
}

struct CheckoutView: View {
    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss
    @Bindable var store: StoreModel

    var body: some View {
        NavigationStack {
            Form {
                Section("Your order") {
                    ForEach(store.products.filter { (store.quantities[$0.id] ?? 0) > 0 }) { p in
                        let q = store.quantities[p.id] ?? 0
                        LabeledContent("\(q) × \(p.name)", value: (p.priceCents * q).dollars)
                    }
                    LabeledContent("Total", value: store.totalCents.dollars).bold()
                }
                Section {
                    TextField("Note for the practice (optional)", text: $store.note, axis: .vertical)
                        .lineLimit(2...4)
                } footer: {
                    Text("\(store.payment) The practice lets you know when your order is ready.")
                }
                if let error = store.error {
                    Text(error).foregroundStyle(.red)
                }
            }
            .navigationTitle("Review order")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Back") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Place order") {
                        Task { if await store.place(appState.api) { dismiss() } }
                    }
                    .disabled(store.placing || store.itemCount == 0)
                }
            }
        }
    }
}

struct OrdersView: View {
    @Environment(AppState.self) private var appState
    let store: StoreModel

    var body: some View {
        List(store.orders) { order in
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline) {
                    Text(order.createdAt.formatted(date: .abbreviated, time: .shortened)).font(.headline)
                    Spacer()
                    Text(order.status.label)
                        .font(.caption2.weight(.medium))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(tint(order.status).opacity(0.15), in: Capsule())
                        .foregroundStyle(tint(order.status))
                }
                ForEach(order.items, id: \.productID) { i in
                    Text("\(i.quantity) × \(i.name)").font(.subheadline)
                }
                Text("Total \(order.totalCents.dollars)").font(.caption).foregroundStyle(.secondary)
                if !order.staffNote.isEmpty {
                    Text(order.staffNote).font(.caption).foregroundStyle(.secondary)
                }
                if order.status == .placed {
                    Button("Cancel order", role: .destructive) {
                        Task { await store.cancel(order, appState.api) }
                    }
                    .font(.caption)
                    .buttonStyle(.borderless)
                }
            }
            .padding(.vertical, 2)
            .privacySensitive()
        }
        .navigationTitle("My orders")
        .refreshable { await store.load(appState.api) }
    }

    private func tint(_ s: SupplyOrder.Status) -> Color {
        switch s {
        case .placed: .orange
        case .ready: .blue
        case .fulfilled: .green
        case .cancelled, .unknown: .gray
        }
    }
}
