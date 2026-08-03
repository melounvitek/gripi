import SwiftUI

struct GatewayEditorView: View {
    @EnvironmentObject private var gatewayStore: GatewayStore
    @Environment(\.dismiss) private var dismiss

    private let gateway: Gateway?
    private let dismissAfterSave: Bool
    @State private var name: String
    @State private var url: String

    init(gateway: Gateway? = nil, dismissAfterSave: Bool = false) {
        self.gateway = gateway
        self.dismissAfterSave = dismissAfterSave
        _name = State(initialValue: gateway?.name ?? "")
        _url = State(initialValue: gateway?.url.absoluteString ?? "")
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Server name", text: $name)
                        .textContentType(.organizationName)
                    TextField("https://gripi.example.com/", text: $url)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                } header: {
                    Text(gateway == nil ? "Add your Gripi gateway" : "Edit server")
                } footer: {
                    Text("The gateway and Pi continue running on the configured computer. Use HTTPS or an encrypted private network for remote access.")
                }

                if let errorMessage = gatewayStore.errorMessage {
                    Section {
                        Text(errorMessage)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle(gateway == nil ? "Connect to Gripi" : "Edit Server")
            .toolbar {
                if dismissAfterSave {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }
                        .fontWeight(.semibold)
                }
            }
        }
        .onDisappear { gatewayStore.clearError() }
    }

    private func save() {
        let saved: Bool
        if let gateway {
            saved = gatewayStore.save(id: gateway.id, name: name, url: url)
        } else {
            saved = gatewayStore.add(name: name, url: url)
        }

        if saved && dismissAfterSave { dismiss() }
    }
}
