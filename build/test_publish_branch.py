"""Exercise publication against disposable local repositories, never GitHub."""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest


class PublicationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="braw-publication-test-")
        self.root = Path(self.temp.name).resolve()
        assert self.root.is_relative_to(Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.temp.cleanup)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.env = os.environ.copy()
        self.env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
                        GIT_AUTHOR_NAME="Test Author", GIT_AUTHOR_EMAIL="author@example.test",
                        GIT_COMMITTER_NAME="Test Committer", GIT_COMMITTER_EMAIL="committer@example.test",
                        GIT_AUTHOR_DATE="2020-01-01T00:00:00 +0000",
                        GIT_COMMITTER_DATE="2020-01-02T00:00:00 +0000")
        self.git("init", "-b", "main")
        (self.repo / "base.txt").write_text("base\n")
        self.git("add", ".")
        self.git("commit", "-m", "Shared base")
        self.base = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", "-b", "macos-beta")
        (self.repo / "build").mkdir()
        shutil.copyfile(Path(__file__).with_name("publish-branch.py"), self.repo / "build/publish-branch.py")
        self.git("add", ".")
        self.git("commit", "-m", "Add publication tool\n\nKeep this body unchanged.")
        self.git("checkout", "-b", "feature")
        (self.repo / "feature.txt").write_text("feature\n")
        self.git("add", ".")
        self.git("commit", "-m", "Feature commit")
        self.git("checkout", "macos-beta")
        (self.repo / "beta.txt").write_text("beta\n")
        self.git("add", ".")
        self.git("commit", "-m", "Beta commit")
        self.git("merge", "--no-ff", "feature", "-m", "Merge feature")
        self.tip = self.git("rev-parse", "HEAD").strip()
        self.remote = self.root / "remote.git"
        self.git("init", "--bare", str(self.remote))
        self.git("remote", "add", "origin", str(self.remote))

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, env=self.env, stderr=subprocess.PIPE)

    def run_helper(self, *args):
        return subprocess.run([os.sys.executable, "build/publish-branch.py", *args],
                              cwd=self.repo, env=self.env, capture_output=True)

    def contents(self, revision):
        header, message = self.git("cat-file", "commit", revision).split(b"\n\n", 1)
        fields = header.splitlines()
        return (next(x for x in fields if x.startswith(b"tree ")), message,
                [x.split(b">", 1)[0] for x in fields if x.startswith((b"author ", b"committer "))],
                len([x for x in fields if x.startswith(b"parent ")]))

    def test_preview_and_publication_preserve_content_and_merge(self):
        preview = self.run_helper()
        self.assertEqual(preview.returncode, 0, preview.stderr)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.tip)
        self.assertEqual(self.git("ls-remote", "--heads", "origin"), b"")
        old = self.git("rev-list", "--reverse", "--topo-order", "main..HEAD").split()
        before = [self.contents(x.decode()) for x in old]
        published = self.run_helper("--publish")
        self.assertEqual(published.returncode, 0, published.stderr)
        new = self.git("rev-list", "--reverse", "--topo-order", "main..HEAD").split()
        self.assertEqual(before, [self.contents(x.decode()) for x in new])
        self.assertNotEqual(old, new)
        dates = self.git("log", "--format=%aI%n%cI", "main..HEAD").splitlines()
        self.assertEqual(len(set(dates)), 1)
        self.assertNotIn(b"2020-01", dates[0])
        self.assertEqual(self.git("rev-parse", "main").strip(), self.base)
        self.assertEqual(self.git("for-each-ref", "--format=%(objectname)", "refs/backups/publication").strip(), self.tip)
        self.assertIn(self.git("rev-parse", "HEAD").strip(), self.git("ls-remote", "--heads", "origin"))
        second = self.run_helper("--publish")
        self.assertNotEqual(second.returncode, 0)
        self.assertIn(b"already exists", second.stderr)

    def test_dirty_worktree_is_rejected_without_mutation(self):
        (self.repo / "beta.txt").write_text("uncommitted change\n")
        result = self.run_helper("--publish")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"tracked changes", result.stderr)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.tip)
        self.assertEqual(self.git("ls-remote", "--heads", "origin"), b"")


if __name__ == "__main__":
    unittest.main()
