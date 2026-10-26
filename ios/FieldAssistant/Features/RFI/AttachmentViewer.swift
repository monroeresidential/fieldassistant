import FieldAssistantAPI
import Foundation
import Observation
import QuickLook
import SwiftUI

typealias RFIAttachment = Components.Schemas.RFIAttachment

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
            let directory = FileManager.default.temporaryDirectory
                .appendingPathComponent("RFIAttachments", isDirectory: true)
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

/// One attachment row on the RFI detail. Tapping it downloads and previews the file; without
/// signal, or on failure, it says so under the name.
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
            HStack(spacing: 12) {
                Image(systemName: "paperclip")
                    .foregroundStyle(RecordPalette.muted)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    Text(displayName)
                        .foregroundStyle(.primary)
                        .lineLimit(2)
                    if let caption {
                        Text(caption)
                            .font(.footnote)
                            .foregroundStyle(RecordPalette.accent)
                    }
                }
                Spacer()
                if model?.state == .loading { ProgressView() }
            }
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(model?.state == .loading)
        .accessibilityLabel("Attachment \(displayName)")
        .accessibilityValue(caption ?? "")
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

    private var caption: String? {
        switch model?.state {
        case .needsSignal: "Needs signal"
        case .failed(let message): message
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
