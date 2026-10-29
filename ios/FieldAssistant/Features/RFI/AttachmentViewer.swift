import FieldAssistantAPI
import Foundation
import Observation
import QuickLook
import SwiftUI

typealias RFIAttachment = Components.Schemas.RFIAttachment

/// Where downloaded RFI attachments are kept. They belong to the signed-in user, so `AppState`
/// removes the directory whenever the session ends.
enum AttachmentFiles {
    static var directory: URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("RFIAttachments", isDirectory: true)
    }

    /// Deletes every downloaded attachment. Nothing to delete is not an error.
    static func removeAll() throws {
        guard FileManager.default.fileExists(atPath: directory.path) else { return }
        try FileManager.default.removeItem(at: directory)
    }
}

/// Fetches one RFI attachment through the backend proxy into a temp file for Quick Look.
/// Needs signal: the bytes are not part of the mirror.
@MainActor
@Observable
final class AttachmentViewerModel {
    enum State: Equatable {
        case idle
        case needsSignal
        case loading
        case ready(URL)
        case failed(String)
    }

    private(set) var state: State = .idle

    @ObservationIgnored let attachment: RFIAttachment
    @ObservationIgnored private let fetch: @Sendable () async throws -> (Data, filename: String?, contentType: String?)

    init(attachment: RFIAttachment, service: RecordService, companyID: Int64, projectID: Int64, rfiID: Int64) {
        self.attachment = attachment
        let attachmentID = attachment.id
        fetch = {
            try await service.attachmentData(
                companyID: companyID, projectID: projectID, rfiID: rfiID, attachmentID: attachmentID
            )
        }
    }

    /// Downloads the attachment, or reports that it needs signal. A second call while loading does nothing.
    func open(reachable: Bool) async {
        guard state != .loading else { return }
        guard reachable else {
            state = .needsSignal
            return
        }
        state = .loading
        do {
            let (data, reported, _) = try await fetch()
            let directory = AttachmentFiles.directory
                .appendingPathComponent(String(attachment.id), isDirectory: true)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let url = directory.appendingPathComponent(Self.fileName(reported: reported, attachment: attachment))
            try data.write(to: url, options: .atomic)
            state = .ready(url)
        } catch {
            state = .failed(APIFailure(error).userMessage)
        }
    }

    /// The name the backend reported, else the attachment's filename, else its name, else its id.
    /// Path separators become underscores so the file stays in its directory.
    static func fileName(reported: String?, attachment: RFIAttachment) -> String {
        let candidates = [reported, attachment.filename, attachment.name]
        let name = candidates.lazy
            .compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .first { !$0.isEmpty } ?? String(attachment.id)
        return name.replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: ":", with: "_")
    }
}

/// One attachment row on the RFI detail, at least 52 pt under a hairline. Tapping it downloads and
/// previews the file; without signal (tertiary) or on failure (accent), it says so under the name.
struct AttachmentRow: View {
    let attachment: RFIAttachment
    let projectID: Int64
    let rfiID: Int64

    @Environment(AppState.self) private var appState
    @State private var model: AttachmentViewerModel?
    @State private var preview: PreviewItem?

    var body: some View {
        Button {
            Task { await open() }
        } label: {
            VStack(spacing: 0) {
                HairlineDivider()
                HStack(spacing: 12) {
                    Image(systemName: "paperclip")
                        .font(.system(size: 16, weight: .medium))
                        .foregroundStyle(Theme.Color.muted)
                        .accessibilityHidden(true)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(displayName)
                            .textStyle(Theme.Text.rowTitle)
                            .foregroundStyle(Theme.Color.textPrimary)
                            .lineLimit(2)
                        if let caption {
                            Text(caption.text)
                                .textStyle(Theme.Text.meta12)
                                .foregroundStyle(caption.isFailure ? Theme.Color.accent : Theme.Color.tertiary)
                        }
                    }
                    Spacer(minLength: 0)
                    if model?.state == .loading { ProgressView().tint(Theme.Color.tertiary) }
                }
                .padding(.vertical, 8)
                .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(model?.state == .loading)
        .accessibilityLabel("Attachment \(displayName)")
        .accessibilityValue(caption?.text ?? "")
        .accessibilityHint("Opens the attachment")
        .sheet(item: $preview) { item in
            NavigationStack {
                QuickLookPreview(url: item.url)
                    .ignoresSafeArea(edges: .bottom)
                    .navigationTitle(displayName)
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { preview = nil }
                                .accessibilityLabel("Close attachment")
                        }
                    }
            }
        }
    }

    private var displayName: String {
        attachment.filename.flatMap { $0.isEmpty ? nil : $0 } ?? (attachment.name.isEmpty ? "Attachment" : attachment.name)
    }

    /// "Needs signal" in tertiary, or the failure message in accent.
    private var caption: (text: String, isFailure: Bool)? {
        switch model?.state {
        case .needsSignal: ("Needs signal", false)
        case .failed(let message): (message, true)
        default: nil
        }
    }

    private func open() async {
        guard let companyID = appState.selectedCompanyID else { return }
        let model = model ?? AttachmentViewerModel(
            attachment: attachment,
            service: RecordService(client: appState.client),
            companyID: companyID, projectID: projectID, rfiID: rfiID
        )
        self.model = model
        await model.open(reachable: appState.reachability.isReachable)
        if case .ready(let url) = model.state { preview = PreviewItem(url: url) }
    }

    private struct PreviewItem: Identifiable {
        let url: URL
        var id: URL { url }
    }
}

/// `QLPreviewController` for one local file.
private struct QuickLookPreview: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ controller: QLPreviewController, context: Context) {
        if context.coordinator.url != url {
            context.coordinator.url = url
            controller.reloadData()
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(url: url) }

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL

        init(url: URL) { self.url = url }

        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }

        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> any QLPreviewItem {
            url as NSURL
        }
    }
}
