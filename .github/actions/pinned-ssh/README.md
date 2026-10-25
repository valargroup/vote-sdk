# Pinned SSH action

Vendored from [appleboy/ssh-action](https://github.com/appleboy/ssh-action/tree/029f5b4aeeeb58fdfe1410a5d17f967dacf36262).
The action inputs and entrypoint are unchanged. The Docker base image is pinned
to its multi-platform manifest digest so replacing the upstream tag cannot
change the executable that receives deployment credentials.

When updating, review the upstream source and image together and retain LICENSE.
