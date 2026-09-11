import unittest
from unittest.mock import patch


class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source, self.target = self.root / "source", self.root / "target"
        for name in ("workbench", "desktop", "browser", "exchange"):
            (self.source / name).mkdir(parents=True)
        for name in ("home", "browser", "exchange"):
            (self.target / name).mkdir(parents=True)

    def write(self, name, content):
        path = self.source / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return path

    def test_conflicts_metadata_and_external_symlinks(self):
        baseline = self.write("workbench/project.txt", "workbench")
        baseline.chmod(0o640)
        os.chown(baseline, 1234, 1235)
        self.write("desktop/project.txt", "desktop")
        self.write("workbench/.config/xfce4/config", "old-xfce")
        self.write("desktop/.config/xfce4/config", "desktop-xfce")
        self.write("desktop/.local/share/keyrings/login.keyring", "secret-fixture")
        self.write("desktop/desktop-only", "retained")
        self.write("browser/Default/Cookies", "cookie-fixture")
        self.write("exchange/downloads/report", "download-fixture")
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "sentinel").write_text("untouched")
        (self.source / "workbench/link").symlink_to(outside, target_is_directory=True)
        self.write("desktop/link/incoming", "must-not-follow-target-link")
        (self.source / "desktop/dangling").symlink_to("/does-not-exist")
        migrate(self.source, self.target)
        home = self.target / "home"
        self.assertEqual((home / "project.txt").read_text(), "workbench")
        self.assertEqual((home / "project.txt").stat().st_uid, 1234)
        self.assertEqual((home / "project.txt").stat().st_gid, 1235)
        self.assertEqual(stat.S_IMODE((home / "project.txt").stat().st_mode), 0o640)
        self.assertEqual((home / ".config/xfce4/config").read_text(), "desktop-xfce")
        self.assertEqual((home / ".local/share/keyrings/login.keyring").read_text(), "secret-fixture")
        self.assertEqual((home / "desktop-only").read_text(), "retained")
        self.assertTrue((home / "link").is_symlink())
        self.assertTrue((home / "dangling").is_symlink())
        self.assertEqual(list(outside.iterdir()), [outside / "sentinel"])
        self.assertEqual((self.target / "browser/Default/Cookies").read_text(), "cookie-fixture")
        self.assertEqual((self.target / "exchange/downloads/report").read_text(), "download-fixture")
        archive, = home.glob("sandbox-migration-conflicts-*")
        self.assertEqual((archive / "workbench/project.txt").stat().st_uid, 1234)
        self.assertEqual((archive / "desktop/project.txt").read_text(), "desktop")
        self.assertEqual((archive / "workbench/.config/xfce4/config").read_text(), "old-xfce")
        self.assertEqual(len(json.loads((archive / "report.json").read_text())), 3)
        self.assertEqual(baseline.read_text(), "workbench")

    def test_missing_source_and_low_space_do_not_touch_target(self):
        marker = self.target / "home/marker"
        marker.write_text("candidate")
        with patch.object(shutil, "disk_usage", return_value=type("Space", (), {"free": 0})()):
            with self.assertRaisesRegex(RuntimeError, "Insufficient space"):
                migrate(self.source, self.target)
        self.assertEqual(marker.read_text(), "candidate")
        (self.source / "desktop").rmdir()
        with self.assertRaisesRegex(RuntimeError, "Missing legacy volume"):
            migrate(self.source, self.target)
        self.assertEqual(marker.read_text(), "candidate")

    def test_interrupted_copy_can_restart_from_originals(self):
        original = self.write("workbench/file", "source")
        with patch.object(shutil, "copyfile", side_effect=OSError("interrupted")):
            with self.assertRaisesRegex(OSError, "interrupted"):
                migrate(self.source, self.target)
        self.assertEqual(original.read_text(), "source")
        migrate(self.source, self.target)
        self.assertEqual((self.target / "home/file").read_text(), "source")
        migrate(self.source, self.target)
        self.assertEqual((self.target / "home/file").read_text(), "source")


unittest.main()
