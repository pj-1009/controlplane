-- An update task carries the name the client asked for. The tenant keeps its
-- current name until the task finishes done; a failed update leaves it as it
-- was. NULL for deploy and destroy tasks.
ALTER TABLE tasks ADD COLUMN requested_name text;
