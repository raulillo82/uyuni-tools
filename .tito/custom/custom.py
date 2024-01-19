# Copyright (c) 2018 SUSE Linux Products GmbH
# SPDX-FileCopyrightText: 2023 SUSE LLC
#
# SPDX-License-Identifier: GPL-2.0-only

"""
Code for building packages in SUSE that need generated code not tracked in git.
"""
import os

from tito.builder import Builder
from tito.common import  info_out, run_command, debug

class SuseGitExtraGenerationBuilder(Builder):

    def _setup_sources(self):

        Builder._setup_sources(self)
        setup_execution_file_name = "setup.sh"
        setup_file_dir = os.path.join(self.git_root, self.relative_project_dir)
        setup_file_path = os.path.join(setup_file_dir, setup_execution_file_name)
        if os.path.exists(setup_file_path):
            info_out("Executing %s" % setup_file_path)
            output = run_command("[[ -x %s ]] && %s" % (setup_file_path, setup_file_path), True)
            filename = output.split('\n')[-1]
        if filename and os.path.exists(os.path.join(setup_file_dir, filename)):
            info_out("Copying %s to %s" % (os.path.join(setup_file_dir, filename), self.rpmbuild_sourcedir))
            run_command("cp %s %s/" % (os.path.join(setup_file_dir, filename), self.rpmbuild_sourcedir), True)
            self.sources.append(os.path.join(self.rpmbuild_sourcedir, filename))

        source_push = os.path.join(setup_file_dir, "push.sh")
        if os.path.exists(source_push):
            push_path = os.path.join(self.rpmbuild_sourcedir, "push.sh")
            run_command("cp %s %s/" % (source_push, self.rpmbuild_sourcedir), True)
            self.sources.append(push_path)

            run_command(f"sed '/^URL: .*$/aSource10000: push.sh' -i {self.spec_file}")
            cleanup = f"\nsed '/^Source10000: push.sh/d' -i $SRPM_PKG_DIR/{self.spec_file_name}"
            with open(push_path, "a") as fd:
                fd.write(cleanup)
