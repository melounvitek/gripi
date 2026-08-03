import Foundation

struct ShareRequest: Identifiable {
    let id = UUID()
    let fileURL: URL
}

enum DownloadDestination {
    static func filename(from suggestedFilename: String) -> String {
        let filename = suggestedFilename
            .replacingOccurrences(of: "\\", with: "/")
            .split(separator: "/")
            .last
            .map(String.init)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""

        guard !filename.isEmpty, filename != ".", filename != ".." else { return "gripi-download" }
        return String(filename.prefix(180))
    }

    static func temporaryURL(for suggestedFilename: String) -> URL {
        let directory = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory.appending(path: filename(from: suggestedFilename))
    }
}
