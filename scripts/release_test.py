import unittest
from release_tags import tags, version
from pathlib import Path
from collect_sources import alpine_branch, archive_path, packages, source_checksums


class ReleaseTests(unittest.TestCase):
    def test_source_checksum_formats(self):
        digest = b"a" * 128
        for recipe in [b'sha512sums="' + digest + b'  source.tar.xz"', b'sha512sums="\n' + digest + b'  source.tar.xz\n"']:
            self.assertEqual(source_checksums(recipe), [(digest, b"source.tar.xz")])

    def test_archive_names_preserve_version_and_architecture(self):
        for arch in ("amd64", "arm64"):
            name = f"sources-v1.0.0-{arch}"
            self.assertEqual(str(archive_path(Path(name))), name + ".tar.gz")

    def test_alpine_branch_follows_inventory(self):
        records = packages("P:alpine-base\nV:3.24.2-r0\n")
        self.assertEqual(alpine_branch(records), "v3.24")
        with self.assertRaises(ValueError):
            alpine_branch(packages("P:alpine-base\nV:edge\n"))

    def test_initial(self):
        self.assertEqual(tags("v1.0.0", False, "v1.0.0", []), ["1.0.0", "1.0", "latest"])

    def test_prerelease(self):
        self.assertEqual(tags("v1.1.0-rc.1", True, "v1.1.0-rc.1", []), ["1.1.0-rc.1"])
        self.assertEqual(tags("v1.1.0-rc.1", False, "v1.1.0-rc.1", []), ["1.1.0-rc.1"])

    def test_older_release(self):
        releases = [{"tag_name": "v1.0.2"}, {"tag_name": "v2.0.0"}]
        self.assertEqual(tags("v1.0.1", False, "v1.0.1", releases), ["1.0.1"])
        self.assertEqual(tags("v1.0.3", False, "v2.0.0", releases), ["1.0.3", "1.0"])

    def test_invalid(self):
        for tag in ["main", "v1.0", "v01.0.0", "v1.0.0;echo bad", "v1.0.0\n"]:
            with self.assertRaises(ValueError):
                version(tag)

    def test_package_inventory(self):
        result = packages("P:ffmpeg\nV:8.0-r0\nL:GPL-3.0\no:ffmpeg\nc:abc\n\nP:musl\nV:1\nL:MIT\n")
        self.assertEqual(len(result), 2)
        self.assertEqual(result[0]["o"], "ffmpeg")
